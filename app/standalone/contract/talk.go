package contract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// This file is the project-scoped half of the wire contract: the conversation a
// project's bus speaks, reached through the gateway at /p/<id>/api/…
//
// The client in client.go owns the control plane (which projects exist, start,
// stop). This one owns one project's conversation: who is on its bus, the input
// a human sends, and the live event stream. Both are plain HTTP over the same
// gateway, so a terminal is just another client — nothing here knows how a
// project is laid out on disk or how its bus is wired.
//
// Like the rest of the package it imports the standard library only, so any
// surface (a terminal on another machine, a script) can depend on it without
// inheriting the single-machine form.

// Worker is one identity a project's bus reports, as GET /p/<id>/api/workers
// lists it. Only the fields a conversation needs are kept; the rest of the
// worker view (ACLs, params, supervision) is management data for the WebUI.
type Worker struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Label  string `json:"label,omitempty"`
	Online bool   `json:"online"`
}

// Event is one bus event as the project's SSE stream delivers it. Payload stays
// an open map: its shape is owned by the event's type (worker.input carries
// llm-like content blocks, reason.response a content array, and so on), so the
// wire layer must not narrow it.
type Event struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	WorkerID       string         `json:"worker_id"`
	TargetWorkerID string         `json:"target_worker_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	TraceID        string         `json:"trace_id,omitempty"`
	Timestamp      int64          `json:"timestamp"`
	Payload        map[string]any `json:"payload,omitempty"`
	Recipients     []string       `json:"recipients,omitempty"`
	// Transient marks live-only events (streaming deltas, partial tool output)
	// the bus never persists. A reader that renders history can skip them.
	Transient bool `json:"transient,omitempty"`
}

// Block is one element of a structured input's "content" array — the same
// llm-like shape the reason worker's input converter understands. Text is the
// only kind a terminal needs; image/file are left to the browser.
type Block struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// Input is the body POST /p/<id>/api/input accepts. Targets addresses the
// message to specific workers (the human's @mentions); Target is the
// single-target spelling older servers still read, and carries the first
// target so such a server does not fall back to a broadcast.
type Input struct {
	Content   []Block  `json:"content,omitempty"`
	Targets   []string `json:"targets,omitempty"`
	Target    string   `json:"target,omitempty"`
	InputMode string   `json:"input_mode,omitempty"`
}

// projectPath builds a project-scoped API path under the gateway's /p/<id>/
// mount. The id is escaped so it cannot widen the route past its one segment.
func projectPath(id, suffix string) string {
	return "/p/" + url.PathEscape(id) + suffix
}

// Workers lists the identities on a project's bus. A stopped project answers
// with its proxy's 502, surfaced verbatim as an error.
func (c *Client) Workers(projectID string) ([]Worker, error) {
	var out []Worker
	if err := c.do("GET", projectPath(projectID, "/api/workers"), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendInput publishes a message as the project's interface worker — the human's
// voice on the bus. The server answers 202 with no body; a refusal (unreachable
// project, interface worker not on the bus) becomes an error carrying its text.
func (c *Client) SendInput(projectID string, in Input) error {
	if len(in.Targets) > 0 && in.Target == "" {
		in.Target = in.Targets[0]
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do("POST", projectPath(projectID, "/api/input"), bytes.NewReader(body), nil)
}

// Stream is a live event subscription to one project's bus, decoded from the
// project's server-sent-events endpoint. Next returns one event at a time; call
// Close when done.
type Stream struct {
	body io.ReadCloser
	r    *bufio.Reader
}

// Follow opens the project's live event stream. The connection lives until ctx
// is cancelled or the stream ends, so it uses a client with no overall timeout
// (the control-plane client's 15 seconds would cut every stream short).
func (c *Client) Follow(ctx context.Context, projectID string) (*Stream, error) {
	transport := http.DefaultTransport
	if c.HTTP != nil && c.HTTP.Transport != nil {
		transport = c.HTTP.Transport
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+projectPath(projectID, "/api/stream"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway %s: %w", c.BaseURL, err)
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		msg := strings.TrimSpace(string(raw))
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("%s", msg)
	}
	return &Stream{body: resp.Body, r: bufio.NewReader(resp.Body)}, nil
}

// Next returns the next event, skipping SSE comments, the up-front watermark
// control frame, and the "event:" names the bus uses for control messages.
func (s *Stream) Next() (Event, error) {
	var (
		data  []string
		event string
	)
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return Event{}, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// A blank line dispatches the frame. A frame with no data is a bare
			// control (e.g. an event: with no payload) and is skipped.
			if len(data) == 0 {
				event = ""
				continue
			}
			payload := strings.Join(data, "\n")
			name := event
			data, event = data[:0], ""
			if name == "watermark" {
				continue // subscription watermark, not a bus event
			}
			var out Event
			if err := json.Unmarshal([]byte(payload), &out); err != nil {
				return Event{}, fmt.Errorf("decode event: %w", err)
			}
			return out, nil
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / heartbeat
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			event = value
		}
	}
}

// Close releases the stream's connection.
func (s *Stream) Close() error {
	if s == nil || s.body == nil {
		return nil
	}
	return s.body.Close()
}
