package reason

import (
	"context"
	"sync"
	"testing"
	"time"

	reasonBase "github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/itfs/event"
	llm "github.com/niq-run/niq/core/itfs/llm"
)

// TestCoreExtensionsRegistered verifies the default worker registers its
// toolkit on the extension registry: send_message / search / context
// ops each as their own event type, and the context ops as self-editing
// meta extensions.
func TestCoreExtensionsRegistered(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()})

	// Own extensions reach the LLM through discovery (the self-directed
	// worker.ready), so the registry is the source of truth to assert against.
	registered := func(evt event.EventType) bool {
		for _, e := range w.Extensions() {
			if e.Event == evt {
				return true
			}
		}
		return false
	}

	if !registered(event.EventType("message")) {
		t.Fatal("message not registered under its own event type")
	}
	if !registered(reasonBase.TypeContextCompress) {
		t.Fatal("context.compress not registered as a self-editing meta extension")
	}
	// search / describe are NOT the reason worker's tools anymore
	// — the fleet directory lives on the HOST worker, so a reason worker does
	// not surface peer-reason capabilities as callable tools.
	if registered("search") || registered("describe") {
		t.Fatal("search / describe must not be registered on the reason worker (host owns the directory)")
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

// TestCoreToolPolicy verifies the reason worker's tool list is exactly the three
// built-in sends — message, request, run_program — and nothing else. Everything
// else (capability workers' tools, the directory's lookups, the worker's own meta
// events) is reachable only through request.
func TestCoreToolPolicy(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()})

	// The self-directed ready carries the full contract (Internal included).
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"watch":     w.WatchEntries(true),
	}))
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, "directory", map[string]any{
		"type":      "directory",
		"worker_id": "directory",
		"watch": []map[string]any{
			{"event": "search"},
			{"event": "describe"},
			{"event": "search"},
		},
	}))
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, "workspace", map[string]any{
		"type":      "workspace",
		"worker_id": "workspace",
		"watch":     []map[string]any{{"event": "bash"}},
	}))
	// A directory worker, and a same-typed one with a non-default id: neither
	// contributes a tool any more — discovery is reached with request.
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, "dir-2", map[string]any{
		"type":      "directory",
		"worker_id": "dir-2",
		"watch":     []map[string]any{{"event": "custom_lookup"}},
	}))

	got := make(map[string]bool)
	for _, d := range w.LLMToolDefs() {
		got[d.Name] = true
	}
	for _, want := range []string{"message", "request", "run_program"} {
		if !got[want] {
			t.Fatalf("expected %q in LLM tool list, got %v", want, keysOf(got))
		}
	}
	for _, unwanted := range []string{
		"directory__search", "directory__describe", "dir-2__custom_lookup",
		"context_compress", "program_query", "program_attach", "program_detach",
		"provider_switch", "provider_list", "provider_current",
		"workspace__bash",
	} {
		if got[unwanted] {
			t.Fatalf("%q must be request-only, got %v", unwanted, keysOf(got))
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

// TestBroadcastReadyExcludesInternal verifies the two-part announcement: the
// presence broadcast excludes this worker and omits Internal extensions,
// while the directed announcement to itself carries the full contract. The
// worker therefore receives exactly one self-sourced ready (its complete view),
// which keeps HandleWorkerReady's whole-source replacement semantics valid.
func TestBroadcastReadyExcludesInternal(t *testing.T) {
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
	if watchHasEvent(*presence, "message") {
		t.Fatal("presence broadcast must not carry the Internal core tools")
	}
	if !watchHasEvent(*presence, "provider.switch") {
		t.Fatal("presence broadcast must keep advertised extensions")
	}
	if !watchHasEvent(*directed, "message") {
		t.Fatal("directed announcement must carry the full contract, Internal included")
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
	// context.rotate is temporarily disabled (registration commented out);
	// skip until the tool is re-enabled.
	t.Skip("context.rotate temporarily disabled")
	prov := &rotateFlowProvider{}
	w, ch, cancel := startWorker(t, prov)
	defer cancel()

	// The self-ready announcement is queued first, FIFO before the input, so
	// the rotate tool resolves through the discovery universe (the mock
	// channel's Send only records; it does not loop back into the watch loop).
	ch.in <- event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"watch":     w.WatchEntries(true),
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

// TestDefaultToolsDeclareResponse verifies the reason toolkit's own generated
// tools describe their reply as well: the model sees them every round, and a
// PTC script can reach them through request, so an undeclared reply there would
// be the blind spot peer tools no longer have.
func TestDefaultToolsDeclareResponse(t *testing.T) {
	w := NewWorker(Config{ID: "w1", Bus: newMockChannel()})
	want := []event.EventType{
		reasonBase.TypeMessage, reasonBase.TypeRequest, reasonBase.TypeContextCompress,
		TypeProgramQuery, TypeProgramAttach, TypeProgramDetach,
	}
	have := map[event.EventType]bool{}
	for _, e := range w.WatchEntries(true) {
		ev, _ := e["event"].(string)
		if _, ok := e["response"]; ok {
			have[event.EventType(ev)] = true
		}
	}
	for _, ev := range want {
		if !have[ev] {
			t.Errorf("%s does not declare a response schema", ev)
		}
	}
}
