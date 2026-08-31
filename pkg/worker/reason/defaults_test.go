package reason

import (
	"testing"

	"github.com/niq-run/niq/core/event"
	llm "github.com/niq-run/niq/core/llm"
	reasonBase "github.com/niq-run/niq/pkg/reason"
)

// TestCoreExtensionsRegistered verifies the default worker registers its
// toolkit on the extension registry: send_message / list_workers as
// tool.request loop-backs and context.compress / context.rotate as their own event types
// meta extensions.
func TestCoreExtensionsRegistered(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()})

	if cap, ok := w.ExtensionByToolName("send_message"); !ok || cap.Event != event.TypeToolRequest {
		t.Fatalf("send_message not registered as tool.request extension: %+v ok=%v", cap, ok)
	}
	if cap, ok := w.ExtensionByToolName("context_compress"); !ok || cap.Event != reasonBase.TypeContextCompress {
		t.Fatalf("context_compress not registered as context.compress extension: %+v ok=%v", cap, ok)
	}
}

// TestCoreExtensionsExposedToLLM verifies the LLM tool list is the union of
// the worker's own exposed capabilities and nothing else — provider
// switch/status are not exposed, and the context meta ops are. The worker
// learns its own capabilities from its directed full-contract announcement,
// processed by handleWorkerReady like any other worker's.
func TestCoreExtensionsExposedToLLM(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()})

	// The self-directed ready carries the full contract (SelfOnly included).
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"watch":     w.ExtensionEntries(),
	}))

	defs := w.LLMToolDefs()
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

// TestBroadcastReadyExcludesSelfOnly verifies the two-part announcement: the
// presence broadcast excludes this worker and omits SelfOnly extensions,
// while the directed announcement to itself carries the full contract. The
// worker therefore receives exactly one self-sourced ready (its complete view),
// which keeps handleWorkerReady's whole-source replacement semantics valid.
func TestBroadcastReadyExcludesSelfOnly(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "w1", Bus: ch})

	w.BroadcastReady()

	watchHasName := func(evt event.Event, name string) bool {
		watch, ok := evt.Payload["watch"].([]map[string]any)
		if !ok {
			return false
		}
		for _, e := range watch {
			if n, _ := e["name"].(string); n == name {
				return true
			}
		}
		return false
	}
	watchHasEvent := func(evt event.Event, typ string) bool {
		watch, ok := evt.Payload["watch"].([]map[string]any)
		if !ok {
			return false
		}
		for _, e := range watch {
			if e["event"] == typ {
				return true
			}
		}
		return false
	}

	// Two ready announcements: a presence broadcast (self excluded) and a
	// directed full contract.
	ready := ch.eventsOf(event.TypeWorkerReady)
	if len(ready) != 2 {
		t.Fatalf("expected 2 ready announcements, got %d", len(ready))
	}
	var presence, directed *event.Event
	for i := range ready {
		if ready[i].ExcludeWorkerID == w.ID() {
			presence = &ready[i]
		} else {
			directed = &ready[i]
		}
	}
	if presence == nil || directed == nil {
		t.Fatal("expected one presence broadcast (self excluded) and one directed announcement")
	}
	if watchHasName(*presence, "send_message") || watchHasName(*presence, "list_workers") {
		t.Fatal("presence broadcast must not carry the SelfOnly core tools")
	}
	if !watchHasEvent(*presence, "provider.switch") {
		t.Fatal("presence broadcast must keep non-SelfOnly extensions")
	}
	if !watchHasName(*directed, "send_message") || !watchHasName(*directed, "list_workers") {
		t.Fatal("directed announcement must carry the full contract, SelfOnly included")
	}
}

// TestMetaExtensionCallAndStripToolCalls verifies meta extension detection
// and that a meta call (which never produces a tool result) is excluded from
// the transcript: MetaExtensionCall flags the response by LLMName, and
// StripToolCalls removes all tool_calls while keeping thinking/text. The
// context.compress / context.rotate meta ops are the default worker's toolkit.
func TestMetaExtensionCallAndStripToolCalls(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()}) // default toolkit registered at construction

	metaMsg := llm.Message{
		Role:       llm.RoleAssistant,
		StopReason: "tool_calls",
		Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "need to compress"},
			{Type: llm.ContentToolCall, ToolCallID: "m1", ToolName: "context_compress"},
			{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "ws-tmp-niq-test__ls"},
		},
	}
	if _, ok := w.MetaExtensionCall(metaMsg); !ok {
		t.Fatal("MetaExtensionCall should detect context_compress")
	}

	stripped := reasonBase.StripToolCalls(metaMsg)
	if len(stripped.Content) != 1 || stripped.Content[0].Type != llm.ContentThinking {
		t.Fatalf("StripToolCalls should keep only thinking, got %+v", stripped.Content)
	}

	plain := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}
	if _, ok := w.MetaExtensionCall(plain); ok {
		t.Fatal("MetaExtensionCall must be false without a meta extension call")
	}
}
