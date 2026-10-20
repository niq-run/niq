package reason

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
)

// ready helper: build a worker.ready announcement for a worker of the given
// type, carrying one watch capability.
func readyFor(workerID, typ, capEvt string) event.Event {
	return event.New(event.TypeWorkerReady, workerID, map[string]any{
		"worker_id": workerID,
		"type":      typ,
		"watch": []map[string]any{{
			"event": condStr(capEvt != "", capEvt, "noop.cap"),
			"desc":  "some capability",
		}},
	})
}

func condStr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// TestPeerReasonReadyIsNotATool verifies the core directory split: a reason
// worker's ready describes events it RESPONDS to, not callable tools for peers
// — so another reason worker must NOT surface a peer reason's extensions as
// tools. Infrastructure workers (here "workspace") still surface as tools.
func TestPeerReasonReadyIsNotATool(t *testing.T) {
	w, _ := newTestWorker2()

	// An infra worker's ready becomes a callable capability (workspace__bash).
	w.HandleWorkerReady(readyFor("workspace", "workspace", "bash"))
	if _, ok := w.capabilityByToolName("workspace__bash"); !ok {
		t.Fatalf("infra workspace__bash should be callable, got discovered=%+v", w.discovered)
	}

	// A peer reason worker's ready must NOT become a callable capability.
	w.HandleWorkerReady(readyFor("peer", "reason", "context.compress"))
	if _, ok := w.capabilityByToolName("peer__context_compress"); ok {
		t.Fatalf("peer reason's context_compress must NOT be callable, got discovered=%+v", w.discovered)
	}
	if len(w.discovered) != 1 {
		t.Fatalf("expected exactly the infra capability, got %d: %+v", len(w.discovered), w.discovered)
	}
}

// TestToolNameOfCapabilityOwn verifies an own capability keeps a bare name with
// inner dots turned to underscores, so it is a valid LLM tool identifier.
func TestToolNameOfCapabilityOwn(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	if got := toolNameOfCapability(w, DiscoveredCapability{Source: w.ID(), Event: "context.compress"}); got != "context_compress" {
		t.Fatalf("own dotted = %q, want context_compress", got)
	}
	if got := toolNameOfCapability(w, DiscoveredCapability{Source: w.ID(), Event: "send_message"}); got != "send_message" {
		t.Fatalf("own bare = %q, want send_message", got)
	}
}

// TestToolNameOfCapabilityPeer verifies a capability backed by another worker
// becomes provider__name, so the worker/tool boundary is unambiguous.
func TestToolNameOfCapabilityPeer(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())
	if got := toolNameOfCapability(w, DiscoveredCapability{Source: "workspace", Event: "bash"}); got != "workspace__bash" {
		t.Fatalf("peer = %q, want workspace__bash", got)
	}
	if got := toolNameOfCapability(w, DiscoveredCapability{Source: "timer", Event: "timeout"}); got != "timer__timeout" {
		t.Fatalf("peer = %q, want timer__timeout", got)
	}
}

// TestHandleWorkerReadyAndGone verifies capabilities are learned from
// worker.ready and forgotten on worker.gone.
func TestHandleWorkerReadyAndGone(t *testing.T) {
	w := newTestWorker(nil, newTestChannel())

	ready := event.New(event.TypeWorkerReady, "workspace", map[string]any{
		"watch": []map[string]any{
			{"event": "bash", "desc": "run a command", "parameters": map[string]any{"type": "object"}},
		},
	})
	w.HandleWorkerReady(ready)

	// Tool is prefixed with the worker ID (encoded as provider__name).
	if _, ok := w.capabilityByToolName("workspace__bash"); !ok {
		t.Fatalf("expected workspace__bash tool, got %+v", w.discovered)
	}

	gone := event.New(event.TypeWorkerGone, "host", map[string]any{"worker_id": "workspace"})
	w.handleWorkerGone(gone)
	if _, ok := w.capabilityByToolName("workspace__bash"); ok {
		t.Fatal("tool should be removed after worker.gone")
	}
}

// TestCoreExtensionsRegistered / TestCoreExtensionsExposedToLLM /
// TestBroadcastReadyExcludesSelfOnly moved to pkg/worker/reason: the
// send_message / request / context.compress toolkit (and its SelfOnly
// announcement behavior) lives in the default worker now, not in the shared
// reason mechanism.

// TestProviderToolsExposedToLLM verifies the worker's own provider.* domain is
// LLM-callable: the self-directed ready contract feeds discovery, and the tool
// list exposes provider_list / provider_current / provider_switch, so the model
// can inspect (and switch) its own model supplier in conversation.
func TestProviderToolsExposedToLLM(t *testing.T) {
	w := newTestWorker(nil, nil)
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"type":  "reason",
		"watch": w.WatchEntries(true),
	}))

	names := map[string]bool{}
	for _, d := range w.LLMToolDefs() {
		names[d.Name] = true
	}
	for _, want := range []string{"provider_list", "provider_current", "provider_switch"} {
		if !names[want] {
			t.Fatalf("own tool %s not exposed to the LLM; have %v", want, names)
		}
	}
}

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

	// The peer tool is discoverable under its encoded name.
	cap, ok := w.capabilityByToolName("cute-assistant__remember")
	if !ok || cap.Source != "cute-assistant" {
		t.Fatalf("expected the peer tool to be discoverable: %+v", cap)
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

// TestReadyReturnsReachTheToolList verifies a peer's declared reply shape is
// learned from worker.ready and surfaces in the LLM tool description, so the
// model knows what a call will return before making it (the basis of PTC: a
// script is written against an expected output).
func TestReadyReturnsReachTheToolList(t *testing.T) {
	w, _ := newTestWorker2()

	evt := event.New(event.TypeWorkerReady, "mcp", map[string]any{
		"worker_id": "mcp",
		"type":      "mcp",
		"watch": []map[string]any{{
			"event": "search",
			"desc":  "search things",
			"parameters": map[string]any{
				"type": "object",
			},
			"returns": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
		}},
	})
	w.HandleWorkerReady(evt)

	caps := w.discoveredCapabilities()
	var found *DiscoveredCapability
	for i := range caps {
		if caps[i].Source == "mcp" && caps[i].Event == "search" {
			found = &caps[i]
		}
	}
	if found == nil {
		t.Fatal("capability not discovered")
	}
	if found.Returns == nil || found.Returns["type"] != "array" {
		t.Fatalf("Returns = %#v, want the declared schema", found.Returns)
	}

	var def *llm.ToolDef
	for _, d := range w.LLMToolDefs() {
		if d.Name == "mcp__search" {
			dd := d
			def = &dd
		}
	}
	if def == nil {
		t.Fatal("mcp__search missing from the tool list")
	}
	if !strings.Contains(def.Description, "Returns (JSON Schema)") || !strings.Contains(def.Description, `"type":"array"`) {
		t.Fatalf("description = %q, want the returns hint", def.Description)
	}
}

// TestReturnsHintIsCapped verifies a huge declared schema cannot bloat every
// request's tool list.
func TestReturnsHintIsCapped(t *testing.T) {
	big := map[string]any{"type": "object", "description": strings.Repeat("x", returnsHintMaxBytes*2)}
	hint := withReturnsHint("desc", big)
	if len(hint) > returnsHintMaxBytes+100 {
		t.Fatalf("hint length %d, want it capped near %d", len(hint), returnsHintMaxBytes)
	}
	if withReturnsHint("desc", nil) != "desc" {
		t.Fatal("no Returns must leave the description untouched")
	}
}
