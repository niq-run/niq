package niw

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestReadmeTextDescribesBridge verifies the guide covers the routing, the
// control tools and the live bindings, and never leaks the credential.
func TestReadmeTextDescribesBridge(t *testing.T) {
	local := newMemCh("niw-readme")
	w := New(Config{
		ID:               "niw-readme",
		Bus:              local,
		Recipient:        "reason.local",
		RemoteRecipient:  "reason.remote",
		RemoteURL:        "http://peer-b:9000",
		RemoteWorkerID:   "from-b",
		RemoteCredential: "super-secret-token",
	})

	text := w.readmeText()
	for _, want := range []string{
		"# niw — bridge between two niq buses",
		"niw.recipient.set",
		"niw.dial",
		"niw.status",
		"reason.local",
		"reason.remote",
		"http://peer-b:9000",
		"configured (not shown)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("readme missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "super-secret-token") {
		t.Fatal("readme leaked the remote credential")
	}
}

// TestReadmeIsServed verifies worker.readme is registered and answered.
func TestReadmeIsServed(t *testing.T) {
	w, local, _ := testWorker("reason.local", "reason.remote")

	evt := event.New(event.TypeWorkerReadme, "peer", nil)
	evt.RequestId = "r1"
	if !w.DispatchExtension(evt) {
		t.Fatal("niw does not serve worker.readme")
	}

	local.mu.Lock()
	defer local.mu.Unlock()
	if len(local.sends) != 1 || local.sends[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected one request.completed, got %+v", local.sends)
	}
	if got := event.ReplyText(local.sends[0].Payload); !strings.Contains(got, "# niw") {
		t.Fatalf("reply = %q, want the guide", got)
	}
}
