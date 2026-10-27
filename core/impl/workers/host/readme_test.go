package host

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// readme dispatches one worker.readme request and returns the reply text.
func readme(t *testing.T, w *HostWorker, ch *recordingChannel) string {
	t.Helper()
	before := len(ch.sent)
	evt := event.New(event.TypeWorkerReadme, "peer", map[string]any{})
	evt.RequestId = "req-readme"
	if !w.DispatchExtension(evt) {
		t.Fatal("host does not serve worker.readme")
	}
	if len(ch.sent) != before+1 || ch.sent[len(ch.sent)-1].Type != event.TypeRequestCompleted {
		t.Fatalf("expected one request.completed, got %+v", ch.sent[before:])
	}
	text, _ := ch.sent[len(ch.sent)-1].Payload["result"].(string)
	return text
}

// TestReadmeDescribesHost verifies worker.readme is registered and describes
// the lifecycle tools, worker.help, and the roster boundary.
func TestReadmeDescribesHost(t *testing.T) {
	ch := &recordingChannel{}
	w := New(Config{ID: "host", Bus: ch})

	text := readme(t, w, ch)
	for _, want := range []string{
		"# host — worker lifecycle",
		"spawn",
		"suspend",
		"resume",
		"worker.help",
		"directory worker",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("readme missing %q:\n%s", want, text)
		}
	}
}

// TestReadmeHonorsDeclarationParams verifies the declaration-level readme keys
// ride through host.Config, matching the other managed workers.
func TestReadmeHonorsDeclarationParams(t *testing.T) {
	ch := &recordingChannel{}
	w := New(Config{
		ID:  "host",
		Bus: ch,
		Params: map[string]any{
			"readme": "You are the {{role}} worker.",
			"role":   "lifecycle",
		},
	})

	if got := readme(t, w, ch); got != "You are the lifecycle worker." {
		t.Fatalf("readme = %q, want the declaration override rendered", got)
	}
}
