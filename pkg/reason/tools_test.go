package reason

import (
	"context"
	"testing"

	"github.com/54c1/niq/core/event"
	llm "github.com/54c1/niq/core/llm"
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

// TestCoreCapabilitiesRegistered verifies the core capabilities are registered
// on the capability registry at construction: the two tools (send_message /
// list_workers) as tool.request loop-backs, and the context meta ops
// (context.compress / context.rotate) as worker.update meta capabilities
// exposed to the LLM by their LLMName.
func TestCoreCapabilitiesRegistered(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())

	if cap, ok := w.capabilityByToolName("send_message"); !ok || cap.Event != event.TypeToolRequest {
		t.Fatalf("send_message not registered as tool.request capability: %+v ok=%v", cap, ok)
	}
	if cap, ok := w.capabilityByToolName("context_compress"); !ok || cap.Event != event.TypeWorkerUpdate {
		t.Fatalf("context_compress not registered as worker.update capability: %+v ok=%v", cap, ok)
	}
}

// TestCoreCapabilitiesExposedToLLM verifies the LLM tool list is the union of
// the worker's exposed capabilities (LLMName set) and nothing else — provider
// switch/status are not exposed, and the context meta ops are.
func TestCoreCapabilitiesExposedToLLM(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())

	defs := w.llmToolDefs()
	got := make(map[string]bool)
	for _, d := range defs {
		got[d.Name] = true
	}
	for _, want := range []string{"send_message", "list_workers", "context_compress", "context_rotate"} {
		if !got[want] {
			t.Fatalf("expected %q in LLM tool list, got %v", want, keysOf(got))
		}
	}
	for _, banned := range []string{"provider.switch", "provider.list", "provider.current"} {
		if got[banned] {
			t.Fatalf("provider capability %q must not be exposed to the LLM", banned)
		}
	}
}

func keysOf(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestToolNameMapRestoresDottedNames verifies that when a worker declares a
// dotted tool name (e.g. program.search), the encoded LLM-facing name is
// recorded in toolNameMap and dispatch hands the worker back its original
// dotted name instead of the encoded/underscore form.
func TestToolNameMapRestoresDottedNames(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)

	// A worker announces a dotted tool name.
	ready := event.New(event.TypeWorkerReady, "program", map[string]any{
		"worker_id": "program",
		"tools": []map[string]any{
			{"name": "program.search", "description": "find a program"},
		},
	})
	w.handleWorkerReady(ready)

	// It is encoded as provider__name for the LLM, with the mapping kept.
	if got := w.toolNameMap["program__program_search"]; got != "program.search" {
		t.Fatalf("toolNameMap = %q, want %q", got, "program.search")
	}

	// Dispatch restores the original dotted name to the provider.
	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "program__program_search"},
	}
	w.mu.Lock()
	w.handleToolCalls(context.Background(), calls, "trace1")

	var gotName string
	for _, e := range ch.eventsOf(event.TypeToolRequest) {
		if n, _ := e.Payload["name"].(string); n != "" {
			gotName = n
		}
	}
	if gotName != "program.search" {
		t.Fatalf("dispatched tool name = %q, want %q", gotName, "program.search")
	}
}
