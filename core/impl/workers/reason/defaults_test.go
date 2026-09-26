package reason

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
	llm "github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/impl/baseworker"
	reasonBase "github.com/niq-run/niq/core/impl/reason"
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
	// list_workers / get_worker_info are NOT the reason worker's tools anymore
	// — the fleet directory lives on the HOST worker, so a reason worker does
	// not surface peer-reason capabilities as callable tools.
	if _, ok := w.ExtensionByToolName("list_workers"); ok {
		t.Fatal("list_workers must not be registered on the reason worker (host owns the directory)")
	}
	if _, ok := w.ExtensionByToolName("get_worker_info"); ok {
		t.Fatal("get_worker_info must not be registered on the reason worker (host owns the directory)")
	}
}

// TestHandleSendMessageSendsAppendInput verifies send_message delivers the
// message as a worker.input event in append mode (input_mode:"append") — a
// gentle wake-up that does not interrupt the target's in-flight reasoning.
func TestHandleSendMessageSendsAppendInput(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "w1", Bus: ch})

	handleSendMessage(w.BaseReasonWorker, "call-1", "send_message", "w1", "trace-1", map[string]any{
		"target": "w2",
		"text":   "hi",
	})

	inputs := ch.eventsOf(event.TypeWorkerInput)
	if len(inputs) != 1 {
		t.Fatalf("sent %d worker.input events, want 1", len(inputs))
	}
	in := inputs[0]
	if in.WorkerId != "w1" {
		t.Fatalf("input from = %q, want w1", in.WorkerId)
	}
	if text, _ := in.Payload["text"].(string); text != "hi" {
		t.Fatalf("text = %q, want hi", text)
	}
	if mode, _ := in.Payload["input_mode"].(string); mode != "append" {
		t.Fatalf("input_mode = %q, want append", mode)
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
	// The fleet roster is NOT an LLM tool here — that is the host's
	// list_workers directory. This worker's own tools are its self tools.
	for _, want := range []string{"send_message", "context_compress", "context_rotate"} {
		if !got[want] {
			t.Fatalf("expected %q in LLM tool list, got %v", want, keysOf(got))
		}
	}
	if got["list_workers"] || got["get_worker_info"] {
		t.Fatalf("list_workers/get_worker_info must not be LLM tools on the reason worker, got %v", keysOf(got))
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
	if watchHasEvent(*presence, "send_message") {
		t.Fatal("presence broadcast must not carry the SelfOnly core tools")
	}
	if !watchHasEvent(*presence, "provider.switch") {
		t.Fatal("presence broadcast must keep non-SelfOnly extensions")
	}
	if !watchHasEvent(*directed, "send_message") {
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

	// compress/rotate are now ordinary self-tools (they pair normally), so a
	// call to them is not an "edit call" and must not be stripped.
	compressMsg := llm.Message{
		Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "need to compress"},
			{Type: llm.ContentToolCall, ToolCallID: "m1", ToolName: "context_compress"},
		},
	}
	if _, ok := w.TranscriptEditCall(compressMsg); ok {
		t.Fatal("context_compress is an ordinary tool and must not be flagged as a transcript-edit call")
	}

	// The transcript-edit mechanism still exists for custom self-editing tools.
	const editEvent event.EventType = "custom_editor.edit"
	w.Register(baseworker.Extension{Event: editEvent, Description: "custom"}, func(evt event.Event) {})
	w.RegisterTranscriptEditEvent(editEvent)

	metaMsg := llm.Message{
		Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "need to compress"},
			{Type: llm.ContentToolCall, ToolCallID: "m1", ToolName: "custom_editor_edit"},
			{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "ws-tmp-niq-test__ls"},
		},
	}
	if _, ok := w.TranscriptEditCall(metaMsg); !ok {
		t.Fatal("TranscriptEditCall should detect a registered transcript-edit call")
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

// rotateFlowProvider serves the three LLM touchpoints of a rotate round: the
// reasoning stream returns the meta tool call, the summarizer (Complete)
// returns the digest text, and the follow-up round returns a plain reply.
type rotateFlowProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *rotateFlowProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return &llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "DIGEST carried summary"}}}}, nil
}

func (p *rotateFlowProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	var msg llm.Message
	if n == 1 {
		msg = llm.Message{Role: llm.RoleAssistant, StopReason: "tool_calls",
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "context_rotate"}}}
	} else {
		msg = llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "rotated"}}}
	}
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(msg)
	return es, nil
}

// TestMetaRotateEchoesToolCallID verifies the model-invoked rotate dispatch
// carries the tool call id as its RequestId and the async completion echoes it
// back — the pairing the talk view relies on to merge a meta request with its
// result. Rotate routes as a normal self-tool, so self-discovery must be in
// place for the call to dispatch.
func TestMetaRotateEchoesToolCallID(t *testing.T) {
	prov := &rotateFlowProvider{}
	w, ch, cancel := startWorker(t, prov)
	defer cancel()

	// The self-ready announcement is queued first, FIFO before the input, so
	// the rotate tool resolves through the discovery universe (the mock
	// channel's Send only records; it does not loop back into the watch loop).
	ch.in <- event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"watch":     w.ExtensionEntries(),
	})
	ch.in <- event.New(event.TypeWorkerInput, "webui-hiw", map[string]any{"text": "rotate please"})

	var rotateEvt event.Event
	waitCond(t, 2*time.Second, func() bool {
		for _, e := range ch.eventsOf(TypeContextRotate) {
			rotateEvt = e
			return true
		}
		return false
	}, "context.rotate dispatch")
	if rotateEvt.RequestId == "" {
		t.Fatal("meta dispatch must carry the tool call id as RequestId")
	}

	// The mock channel logs self-directed sends without delivering them, so
	// feed the dispatched event back the way the real bus would.
	ch.in <- rotateEvt

	waitCond(t, 2*time.Second, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if e.RequestId == rotateEvt.RequestId {
				return true
			}
		}
		return false
	}, "request.completed echoing the meta request id")
}

func (p *rotateFlowProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }
