package workspace

import (
	"strings"
	"testing"

	backend "github.com/niq-run/niq/core/impl/embedws"
	"github.com/niq-run/niq/core/itfs/event"
)

// TestReadmeTextDescribesWorkspace verifies the guide covers the tool groups
// and reports the live mounts, mode and approver.
func TestReadmeTextDescribesWorkspace(t *testing.T) {
	primary := t.TempDir()
	b := backend.NewEmbeddedBackend([]string{primary})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-readme", Bus: ch, Backend: b, Approver: "webui-hiw"})
	w.buildHandlers()

	text := w.readmeText()
	for _, want := range []string{
		"# workspace — files and shell",
		"read { path",
		"bash { command",
		"mount.add",
		"mode.set",
		"- mode: readwrite",
		primary,
		"webui-hiw",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("readme missing %q:\n%s", want, text)
		}
	}
}

// TestReadmeIsServed verifies worker.readme is registered and answered.
func TestReadmeIsServed(t *testing.T) {
	b := backend.NewEmbeddedBackend([]string{t.TempDir()})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-readme", Bus: ch, Backend: b})
	w.buildHandlers()

	evt := event.New(event.TypeWorkerReadme, "peer", nil)
	evt.RequestId = "r1"
	if !w.DispatchExtension(evt) {
		t.Fatal("workspace does not serve worker.readme")
	}
	if got := ch.lastReplyType(); got != event.TypeRequestCompleted {
		t.Fatalf("reply = %s, want request.completed", got)
	}
}
