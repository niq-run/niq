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

// TestCoreExtensionsRegistered / TestCoreExtensionsExposedToLLM /
// TestBroadcastReadyExcludesSelfOnly moved to pkg/worker/reason: the
// send_message / list_workers / context.compress / context.rotate toolkit (and
// its SelfOnly announcement behavior) lives in the default worker now, not in
// the shared reason mechanism.
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

// TestPeerWatchToolDispatchable verifies a peer's tool.request capability
// announced via the watch (the reason-worker discovery channel) is not just
// exposed to the LLM but also dispatchable: the encoded source__name lives in
// the dispatch table, and a tool call routes back to the declaring worker with
// its original name. Regression for "tool call unavailable: source__tool".
func TestPeerWatchToolDispatchable(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)

	// A peer reason worker announces a custom tool as a tool.request capability.
	ready := event.New(event.TypeWorkerReady, "cute-assistant", map[string]any{
		"worker_id": "cute-assistant",
		"watch": []map[string]any{
			{"event": "tool.request", "name": "remember", "desc": "Remember a fact",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{}}},
		},
	})
	w.handleWorkerReady(ready)

	// The encoded name is in the dispatch table, mapped back to the original.
	if got := w.toolNameMap["cute-assistant__remember"]; got != "remember" {
		t.Fatalf("toolNameMap = %q, want remember", got)
	}
	tool, ok := w.tools["cute-assistant__remember"]
	if !ok || tool.Provider != "cute-assistant" {
		t.Fatalf("dispatch table missing peer tool: %+v", tool)
	}

	// The LLM-facing name is exactly the dispatch key.
	found := false
	for _, d := range w.llmToolDefs() {
		if d.Name == "cute-assistant__remember" {
			found = true
		}
	}
	if !found {
		t.Fatal("peer tool not exposed to the LLM under the dispatch name")
	}

	// A tool call routes back to the declaring worker with the original name.
	w.mu.Lock()
	w.handleToolCalls(context.Background(), []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "cute-assistant__remember"},
	}, "trace1")

	var gotName string
	for _, e := range ch.eventsOf(event.TypeToolRequest) {
		if n, _ := e.Payload["name"].(string); n != "" {
			gotName = n
		}
	}
	if gotName != "remember" {
		t.Fatalf("dispatched tool name = %q, want remember", gotName)
	}
}
