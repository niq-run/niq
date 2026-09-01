package reason

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/event"
	llm "github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/worker"
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
	if got := encodeToolName(w, worker.Tool{Name: "timeout", Provider: "timer"}); got != "timer__timeout" {
		t.Fatalf("timer encode = %q, want timer__timeout", got)
	}
}

// TestHandleWorkerReadyAndGone verifies capabilities and published events are
// learned from worker.ready and forgotten on worker.gone.
func TestHandleWorkerReadyAndGone(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())

	ready := event.New(event.TypeWorkerReady, "workspace", map[string]any{
		"worker_id": "workspace",
		"watch": []map[string]any{
			{"event": "bash", "desc": "run a command", "parameters": map[string]any{"type": "object"}},
		},
		"publishes": []map[string]any{
			{"type": "fs.changed", "description": "a file changed"},
		},
	})
	w.HandleWorkerReady(ready)

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

// TestCoreExtensionsRegistered / TestCoreExtensionsExposedToLLM /
// TestBroadcastReadyExcludesSelfOnly moved to pkg/worker/reason: the
// send_message / list_workers / context.compress / context.rotate toolkit (and
// its SelfOnly announcement behavior) lives in the default worker now, not in
// the shared reason mechanism.
// TestPeerDottedToolDispatches verifies a peer's dotted event type
// (program.search) is exposed to the LLM as provider__program_search and a
// call routes back to the peer as the event type itself.
func TestPeerDottedToolDispatches(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)

	// A peer announces a dotted event-type tool.
	ready := event.New(event.TypeWorkerReady, "program", map[string]any{
		"worker_id": "program",
		"watch":     []map[string]any{{"event": "program.search", "desc": "find a program"}},
	})
	w.HandleWorkerReady(ready)

	// Exposed to the LLM under provider__name (dots -> underscores).
	found := false
	for _, d := range w.LLMToolDefs() {
		if d.Name == "program__program_search" {
			found = true
		}
	}
	if !found {
		t.Fatal("dotted peer tool not exposed to the LLM under provider__name")
	}

	// A call routes back as the event type.
	w.mu.Lock()
	w.handleToolCalls(context.Background(), []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "program__program_search"},
	}, "trace1")

	foundEvt := false
	for _, e := range ch.eventsOf("program.search") {
		if e.RequestId == "c1" {
			foundEvt = true
		}
	}
	if !foundEvt {
		t.Fatal("expected a program.search event to the program worker")
	}
}

// TestPeerToolDispatchable verifies a peer's event-typed capability is exposed
// to the LLM under provider__name and a tool call routes back to the declaring
// worker as its own event type. Regression for "tool call unavailable:
// source__tool".
func TestPeerToolDispatchable(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)

	// A peer worker announces an event-typed capability via the watch channel.
	ready := event.New(event.TypeWorkerReady, "cute-assistant", map[string]any{
		"worker_id": "cute-assistant",
		"watch": []map[string]any{
			{"event": "remember", "desc": "Remember a fact",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		},
	})
	w.HandleWorkerReady(ready)

	// The encoded name is in the dispatch table.
	tool, ok := w.tools["cute-assistant__remember"]
	if !ok || tool.Provider != "cute-assistant" {
		t.Fatalf("dispatch table missing peer tool: %+v", tool)
	}

	// The LLM-facing name is exactly the dispatch key.
	found := false
	for _, d := range w.LLMToolDefs() {
		if d.Name == "cute-assistant__remember" {
			found = true
		}
	}
	if !found {
		t.Fatal("peer tool not exposed to the LLM under the dispatch name")
	}

	// A tool call routes back to the declaring worker as its own event type.
	w.mu.Lock()
	w.handleToolCalls(context.Background(), []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "cute-assistant__remember"},
	}, "trace1")

	foundEvt := false
	for _, e := range ch.eventsOf("remember") {
		if e.RequestId == "c1" {
			foundEvt = true
		}
	}
	if !foundEvt {
		t.Fatal("expected a remember event to the declaring worker")
	}
}
