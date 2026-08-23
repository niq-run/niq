package reason

import (
	"testing"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// TestEncodeToolNameBuiltin verifies a tool whose provider is this worker (or
// empty) keeps a bare name, with inner dots turned to underscores, so it is a
// valid LLM tool identifier.
func TestEncodeToolNameBuiltin(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	if got := encodeToolName(w, worker.Tool{Name: "context.compress", Provider: w.ID()}); got != "context_compress" {
		t.Fatalf("builtin encode = %q, want context_compress", got)
	}
	if got := encodeToolName(w, worker.Tool{Name: "send_message", Provider: ""}); got != "send_message" {
		t.Fatalf("bare encode = %q, want send_message", got)
	}
}

// TestEncodeToolNameExternal verifies a tool backed by another worker becomes
// provider__name, so the worker/tool boundary is unambiguous.
func TestEncodeToolNameExternal(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	if got := encodeToolName(w, worker.Tool{Name: "bash", Provider: "workspace"}); got != "workspace__bash" {
		t.Fatalf("external encode = %q, want workspace__bash", got)
	}
	if got := encodeToolName(w, worker.Tool{Name: "set_tool_timeout", Provider: "timer"}); got != "timer__set_tool_timeout" {
		t.Fatalf("timer encode = %q, want timer__set_tool_timeout", got)
	}
}

// TestHandleWorkerReadyAndGone verifies tools and published events are learned
// from worker.ready and forgotten on worker.gone.
func TestHandleWorkerReadyAndGone(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())

	ready := event.New(event.TypeWorkerReady, "workspace", map[string]any{
		"worker_id": "workspace",
		"tools": []map[string]any{
			{"name": "bash", "description": "run a command", "parameters": map[string]any{"type": "object"}},
		},
		"publishes": []map[string]any{
			{"type": "fs.changed", "description": "a file changed"},
		},
	})
	w.handleWorkerReady(ready)

	// Tool is prefixed with the worker ID (encoded as provider__name).
	if _, ok := w.tools["workspace__bash"]; !ok {
		t.Fatalf("expected workspace__bash tool, got %+v", keys(w.tools))
	}
	if _, ok := w.publishMap["workspace"]; !ok {
		t.Fatal("expected published events for workspace")
	}

	gone := event.New(event.TypeWorkerGone, "host", map[string]any{"worker_id": "workspace"})
	w.handleWorkerGone(gone)
	if _, ok := w.tools["workspace__bash"]; ok {
		t.Fatal("tool should be removed after worker.gone")
	}
	if _, ok := w.publishMap["workspace"]; ok {
		t.Fatal("publishes should be removed after worker.gone")
	}
}

func keys(m map[string]worker.Tool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// loadSelfTools simulates reason discovering its own tool declarations: it
// feeds a worker.ready directed to self (carrying selfToolDeclarations) into
// handleWorkerReady, which loads the tools/meta-tools into w.tools home same
// as any other worker's announcement.
func loadSelfTools(t *testing.T, w *BaseReasonWorker) {
	t.Helper()
	ready := event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"tools":     w.selfToolDeclarations(),
	})
	w.handleWorkerReady(ready)
}

// TestSelfDeclaredToolsLoadable verifies reason's own tools (including meta
// tools) are discovered from a worker.ready directed to self, and that meta
// tools carry the IsMetaTool flag.
func TestSelfDeclaredToolsLoadable(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	loadSelfTools(t, w)

	if _, ok := w.tools["send_message"]; !ok {
		t.Fatalf("send_message should be discovered from self ready, got %+v", keys(w.tools))
	}
	if _, ok := w.tools["context_compress"]; !ok {
		t.Fatalf("context_compress should be discovered, got %+v", keys(w.tools))
	}
	mt := w.tools["context_compress"]
	if !mt.IsMetaTool {
		t.Fatal("context_compress should be marked IsMetaTool from its declaration")
	}
}

// TestDefaultProviderExposesDefaultTools verifies the default provider exposes
// exactly the domain-agnostic tool set, (self-)discovered by this worker.
func TestDefaultProviderExposesDefaultTools(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	loadSelfTools(t, w)

	want := map[string]bool{"send_message": true, "list_workers": true,
		"list_llm_providers": true, "context_compress": true, "context_rotate": true}
	for name := range want {
		if _, ok := w.tools[name]; !ok {
			t.Fatalf("expected exposed tool %q, got %+v", name, keys(w.tools))
		}
	}
	if len(w.tools) != len(want) {
		t.Fatalf("expected exactly %d exposed tools, got %+v", len(want), keys(w.tools))
	}
}
