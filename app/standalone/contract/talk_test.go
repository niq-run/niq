package contract

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWorkersPathAndDecode pins the project-scoped path and the fields the
// terminal keeps: id, type, label and presence.
func TestWorkersPathAndDecode(t *testing.T) {
	r := &recorder{body: `[{"id":"reason","type":"reason","label":"Reasoner","online":true},{"id":"webui","type":"iface","online":true}]`}
	var got []Worker
	if err := r.call(t, func(c *Client) error {
		w, err := c.Workers("demo")
		got = w
		return err
	}); err != nil {
		t.Fatalf("Workers: %v", err)
	}
	if r.method != http.MethodGet || r.path != "/p/demo/api/workers" {
		t.Fatalf("request = %s %s", r.method, r.path)
	}
	if len(got) != 2 || got[0].ID != "reason" || got[0].Label != "Reasoner" || !got[0].Online {
		t.Fatalf("workers = %+v", got)
	}
}

// TestSendInputPostsConversation checks the body a message becomes: content
// blocks for the text, the target list, and the single-target mirror such an
// older server reads so it does not broadcast.
func TestSendInputPostsConversation(t *testing.T) {
	var (
		method, path string
		body         Input
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		method, path = req.Method, req.URL.EscapedPath()
		json.NewDecoder(req.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(srv.Close)

	err := New(srv.URL).SendInput("demo", Input{
		Content: []Block{{Type: "text", Text: "hello"}},
		Targets: []string{"reason"},
	})
	if err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if method != http.MethodPost || path != "/p/demo/api/input" {
		t.Fatalf("request = %s %s", method, path)
	}
	if len(body.Content) != 1 || body.Content[0].Type != "text" || body.Content[0].Text != "hello" {
		t.Fatalf("content = %+v", body.Content)
	}
	if len(body.Targets) != 1 || body.Targets[0] != "reason" {
		t.Fatalf("targets = %+v", body.Targets)
	}
	if body.Target != "reason" {
		t.Fatalf("target mirror = %q, want reason", body.Target)
	}
}

// TestSendInputErrorCarriesMessage keeps the project's refusal intact: "the
// webui worker is not on the bus" must reach the terminal as written.
func TestSendInputErrorCarriesMessage(t *testing.T) {
	r := &recorder{status: http.StatusBadGateway, body: "project demo is not running\n"}
	err := r.call(t, func(c *Client) error {
		return c.SendInput("demo", Input{Content: []Block{{Type: "text", Text: "hi"}}, Targets: []string{"a"}})
	})
	if err == nil || err.Error() != "project demo is not running" {
		t.Fatalf("err = %v", err)
	}
}

// sseServer serves a fixed body as text/event-stream.
func sseServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFollowParsesSSE pins the stream decoder: the watermark control frame and
// heartbeat comments are skipped, and a two-line data frame reassembles into one
// event.
func TestFollowParsesSSE(t *testing.T) {
	srv := sseServer(t, strings.Join([]string{
		"event: watermark",
		"data: wm-1",
		"",
		": ping",
		"",
		`data: {"id":"e1","type":"worker.input","worker_id":"webui"}`,
		"",
		"data: {\"id\":\"e2\",",
		`data: "type":"reason.response","worker_id":"reason"}`,
		"",
	}, "\n")+"\n")

	st, err := New(srv.URL).Follow(context.Background(), "demo")
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	defer st.Close()

	e1, err := st.Next()
	if err != nil {
		t.Fatalf("Next 1: %v", err)
	}
	if e1.ID != "e1" || e1.Type != "worker.input" {
		t.Fatalf("e1 = %+v", e1)
	}
	e2, err := st.Next()
	if err != nil {
		t.Fatalf("Next 2: %v", err)
	}
	if e2.ID != "e2" || e2.Type != "reason.response" {
		t.Fatalf("e2 = %+v", e2)
	}
	if _, err := st.Next(); err != io.EOF {
		t.Fatalf("end err = %v, want EOF", err)
	}
}

// TestFollowSurfaceStatusError checks the 502 a stopped project returns becomes
// the stream open error, not a silent empty one.
func TestFollowSurfaceStatusError(t *testing.T) {
	r := &recorder{status: http.StatusBadGateway, body: "project demo is not running\n"}
	err := r.call(t, func(c *Client) error {
		_, err := c.Follow(context.Background(), "demo")
		return err
	})
	if err == nil || err.Error() != "project demo is not running" {
		t.Fatalf("err = %v", err)
	}
	if r.path != "/p/demo/api/stream" {
		t.Fatalf("path = %q", r.path)
	}
}
