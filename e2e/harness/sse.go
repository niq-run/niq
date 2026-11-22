package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// Stream is an observer on a project's event stream — the same SSE feed the
// browser reads, reached through the gateway's proxy so the test also covers
// that hop.
type Stream struct {
	mu     sync.Mutex
	events []event.Event
	stop   context.CancelFunc
}

// Watch subscribes to a project's stream. query is appended to the stream URL,
// so a test can filter server-side ("type=worker.input") or read everything.
func (g *Gateway) Watch(t *testing.T, projectID, query string) *Stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := g.URL() + "/p/" + projectID + "/api/stream"
	if query != "" {
		url += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		t.Fatalf("build stream request: %v", err)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("open stream: %d", resp.StatusCode)
	}

	s := &Stream{stop: cancel}
	ready := make(chan struct{})
	go func() {
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		first := true
		for scanner.Scan() {
			line := scanner.Text()
			if first {
				// The stream's first frame is the watermark control event, which
				// tells a client where live delivery starts. Once it lands the
				// subscription is live, so a test may publish immediately after.
				close(ready)
				first = false
			}
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var evt event.Event
			if json.Unmarshal([]byte(payload), &evt) != nil || evt.ID == "" {
				continue // the watermark's payload is an id, not an event
			}
			s.mu.Lock()
			s.events = append(s.events, evt)
			s.mu.Unlock()
		}
	}()

	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the event stream never sent its watermark")
	}
	t.Cleanup(cancel)
	return s
}

// Events is everything observed so far.
func (s *Stream) Events() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.Event(nil), s.events...)
}

// Await waits for an observed event matching pred.
func (s *Stream) Await(t *testing.T, what string, timeout time.Duration, pred func(event.Event) bool) event.Event {
	t.Helper()
	var found event.Event
	WaitFor(t, what, timeout, func() bool {
		for _, evt := range s.Events() {
			if pred(evt) {
				found = evt
				return true
			}
		}
		return false
	})
	return found
}
