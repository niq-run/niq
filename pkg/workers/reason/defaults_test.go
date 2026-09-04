package reason

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/event"
	llm "github.com/niq-run/niq/core/llm"
	reasonBase "github.com/niq-run/niq/pkg/reason"
)

// TestCoreExtensionsRegistered verifies the default worker registers its
// toolkit on the extension registry: send_message / list_workers / context
// ops each as their own event type, and the context ops as self-editing
// meta extensions.
func TestCoreExtensionsRegistered(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()})

	if cap, ok := w.ExtensionByToolName("send_message"); !ok || cap.Event != event.EventType("send_message") {
		t.Fatalf("send_message not registered under its own event type: %+v ok=%v", cap, ok)
	}
	if cap, ok := w.ExtensionByToolName("context_compress"); !ok || cap.Event != reasonBase.TypeContextCompress {
		t.Fatalf("context_compress not registered as context.compress extension: %+v ok=%v", cap, ok)
	}
	if cap, ok := w.ExtensionByToolName("get_worker_info"); !ok || cap.Event != TypeWorkerInfo {
		t.Fatalf("get_worker_info not registered under its own event type: %+v ok=%v", cap, ok)
	}
}

// TestCoreExtensionsExposedToLLM verifies the LLM tool list is the union of
// the worker's own exposed capabilities and nothing else — provider
// switch/status are not exposed, and the context meta ops are. The worker
// learns its own capabilities from its directed full-contract announcement,
// processed by HandleWorkerReady like any other worker's.
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
	// The provider.* domain is LLM-callable since the exclusion was dropped:
	// the model may inspect (and switch) its own model supplier. Tool names
	// are the event types with dots → underscores.
	for _, want := range []string{"provider_switch", "provider_list", "provider_current"} {
		if !got[want] {
			t.Fatalf("provider tool %q should be exposed to the LLM, got %v", want, keysOf(got))
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

// TestGetWorkerInfoReturnsFullSchemas verifies the detail view behind the
// slimmed list_workers: a known worker comes back with its COMPLETE tool
// parameter schemas (the part list_workers omits), and an unknown worker
// fails with the self-correcting hint to call list_workers.
func TestGetWorkerInfoReturnsFullSchemas(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "w1", Bus: ch})

	// A peer announces a tool carrying a parameter schema.
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, "timer", map[string]any{
		"worker_id": "timer",
		"watch": []map[string]any{{
			"event":      "timer.timeout",
			"desc":       "set a timeout",
			"parameters": map[string]any{"duration_ms": map[string]any{"type": "integer"}},
		}},
	}))

	handleWorkerInfo(w.BaseReasonWorker, "call-1", "get_worker_info", "webui", "trace-1", map[string]any{"worker": "timer"})
	done := ch.eventsOf(event.TypeRequestCompleted)
	if len(done) != 1 {
		t.Fatalf("expected 1 completed reply, got %d", len(done))
	}
	if res, _ := done[0].Payload["result"].(string); !strings.Contains(res, `"duration_ms"`) {
		t.Fatalf("result must carry the full parameter schema, got %s", res)
	}

	// Missing parameter → failed reply.
	handleWorkerInfo(w.BaseReasonWorker, "call-2", "get_worker_info", "webui", "trace-1", map[string]any{})
	if failed := ch.eventsOf(event.TypeRequestFailed); len(failed) != 1 {
		t.Fatalf("expected 1 failed reply for the missing parameter, got %d", len(failed))
	}

	// Unknown worker → failed reply pointing back at list_workers.
	handleWorkerInfo(w.BaseReasonWorker, "call-3", "get_worker_info", "webui", "trace-1", map[string]any{"worker": "ghost"})
	failed := ch.eventsOf(event.TypeRequestFailed)
	if len(failed) != 2 {
		t.Fatalf("expected 2 failed replies total, got %d", len(failed))
	}
	if msg, _ := failed[1].Payload["error"].(string); !strings.Contains(msg, "list_workers") {
		t.Fatalf("unknown-worker error should point at list_workers, got %q", msg)
	}
}

// TestBroadcastReadyExcludesSelfOnly verifies the two-part announcement: the
// presence broadcast excludes this worker and omits SelfOnly extensions,
// while the directed announcement to itself carries the full contract. The
// worker therefore receives exactly one self-sourced ready (its complete view),
// which keeps HandleWorkerReady's whole-source replacement semantics valid.
func TestBroadcastReadyExcludesSelfOnly(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "w1", Bus: ch})

	w.BroadcastReady()

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
	if watchHasEvent(*presence, "send_message") || watchHasEvent(*presence, "list_workers") {
		t.Fatal("presence broadcast must not carry the SelfOnly core tools")
	}
	if !watchHasEvent(*presence, "provider.switch") {
		t.Fatal("presence broadcast must keep non-SelfOnly extensions")
	}
	if !watchHasEvent(*directed, "send_message") || !watchHasEvent(*directed, "list_workers") {
		t.Fatal("directed announcement must carry the full contract, SelfOnly included")
	}
}

// TestTranscriptEditCallAndStripToolCalls verifies transcript-edit detection
// and that a meta call (which never produces a tool result) is excluded from
// the transcript: TranscriptEditCall flags the response by LLMName, and
// StripToolCalls removes all tool_calls while keeping thinking/text. The
// context.compress / context.rotate meta ops are the default worker's toolkit.
func TestTranscriptEditCallAndStripToolCalls(t *testing.T) {
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
	if _, ok := w.TranscriptEditCall(metaMsg); !ok {
		t.Fatal("TranscriptEditCall should detect context_compress")
	}

	stripped := reasonBase.StripToolCalls(metaMsg)
	if len(stripped.Content) != 1 || stripped.Content[0].Type != llm.ContentThinking {
		t.Fatalf("StripToolCalls should keep only thinking, got %+v", stripped.Content)
	}

	plain := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}
	if _, ok := w.TranscriptEditCall(plain); ok {
		t.Fatal("TranscriptEditCall must be false without a transcript-edit call")
	}
}
