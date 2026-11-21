//go:build e2e

package worker_e2e

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EReasonTurnReachesTheModelAndAnswers is the whole reasoning path with a
// stub in place of a model: the human's message goes in through the app's own
// endpoint, the reason worker builds a real request, the adapter speaks HTTP to
// the stub, and the answer comes back as an event.
func TestE2EReasonTurnReachesTheModelAndAnswers(t *testing.T) {
	t.Parallel()
	stub := harness.NewStubModel(t, "hello from the stub")

	s := harness.NewSandbox(t)
	harness.WriteProviderTo(t, s, stub.BaseURL())
	g := s.Start(t)
	const project = "reason"
	g.CreateProject(t, project)
	g.WaitProject(t, project, true)

	stream := g.Watch(t, project, "")

	code, body := g.PostJSON(t, "/p/"+project+"/api/input", map[string]any{
		"content": []map[string]any{{"type": "text", "text": "ping the stub"}},
		"targets": []string{"niq"},
	})
	if code != http.StatusAccepted {
		t.Fatalf("input: %d %s", code, body)
	}

	reply := stream.Await(t, "the model's answer", 45*time.Second, func(evt event.Event) bool {
		return evt.Type == "reason.response"
	})
	if got := payloadOf(reply); !containsText(got, "hello from the stub") {
		t.Fatalf("the answer is not the stub's: %s", got)
	}

	// The worker really spoke the protocol to the provider we pointed it at.
	reqs := stub.Requests()
	if len(reqs) == 0 {
		t.Fatal("the provider was never called")
	}
	raw, _ := json.Marshal(reqs[0])
	if !containsText(string(raw), "ping the stub") {
		t.Fatalf("the model request does not carry the message: %s", raw)
	}
}
