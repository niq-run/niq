//go:build e2e

package worker_e2e

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/e2e/harness"
)

// newProject brings up an isolated installation with one project running the
// default template: reason, timer, host, directory, history, program.
func newProject(t *testing.T) (*harness.Sandbox, *harness.Gateway, string) {
	t.Helper()
	s := harness.NewSandbox(t)
	harness.WriteProvider(t, s)
	g := s.Start(t)
	const id = "p"
	g.CreateProject(t, id)
	g.WaitProject(t, id, true)
	return s, g, id
}

// sendEvent asks a worker to serve one tool invocation, the way the UI does,
// and returns the request id the bus will pair the reply with.
func sendEvent(t *testing.T, g *harness.Gateway, project, workerID, typ string, payload map[string]any) string {
	t.Helper()
	code, body := g.PostJSON(t, "/p/"+project+"/api/workers/"+workerID+"/event", map[string]any{
		"type":    typ,
		"payload": payload,
	})
	if code != 200 {
		t.Fatalf("send %s to %s: %d %s", typ, workerID, code, body)
	}
	var res struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil || res.RequestID == "" {
		t.Fatalf("event reply = %q (%v)", body, err)
	}
	return res.RequestID
}

// payloadOf renders an event's payload as JSON, for assertions that only care
// that a value is in there somewhere — reply shapes are the worker's business.
func payloadOf(evt event.Event) string {
	raw, err := json.Marshal(evt.Payload)
	if err != nil {
		return ""
	}
	return string(raw)
}

// repliesTo reports whether evt is a completed reply to the given request.
func repliesTo(id string) func(event.Event) bool {
	return func(evt event.Event) bool {
		return evt.RequestId == id && evt.Type == event.TypeRequestCompleted
	}
}

// mountPath is a directory inside the sandbox, for workspace mounts.
func mountPath(s *harness.Sandbox, name string) string {
	return filepath.Join(s.Home, "mounts", name)
}

// mustContain fails unless the rendered payload contains every needle.
func mustContain(t *testing.T, evt event.Event, needles ...string) {
	t.Helper()
	got := payloadOf(evt)
	for _, n := range needles {
		if !strings.Contains(got, n) {
			t.Fatalf("reply payload does not contain %q: %s", n, got)
		}
	}
}
