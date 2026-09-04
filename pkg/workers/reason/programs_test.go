package reason

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/program"
)

const progTestTimeout = 2 * time.Second

// selfLoopChannel wraps mockChannel to model the engine's directed-delivery
// semantics: a Send addressed to the worker itself lands in its watch mailbox
// (production routes it there; plain mockChannel only records). Broadcasts
// stay record-only, matching what the assertions rely on.
type selfLoopChannel struct {
	*mockChannel
}

func (c *selfLoopChannel) Send(_ context.Context, evt event.Event, targets ...string) error {
	if err := c.mockChannel.Send(context.Background(), evt, targets...); err != nil {
		return err
	}
	for _, t := range targets {
		if t == "r1" {
			c.in <- evt
		}
	}
	return nil
}

// TestProgramQueryViaBus drives program.query through the real dispatch path
// (watch loop → process → DispatchExtension → handler). This is the path the
// direct method tests miss: process holds w.mu for the whole dispatch, so a
// handler that takes w.mu itself self-deadlocks the event loop — exactly the
// bug this guards against (the worker would hang and no reply would appear).
func TestProgramQueryViaBus(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	ch.in <- event.New(TypeProgramQuery, "tester", map[string]any{})
	waitCond(t, progTestTimeout, func() bool {
		return len(ch.eventsOf(event.TypeRequestCompleted)) > 0
	}, "request.completed reply for program.query")

	completed := ch.eventsOf(event.TypeRequestCompleted)[0]
	res, _ := completed.Payload["result"].(string)
	var programs []program.Program
	if err := json.Unmarshal([]byte(res), &programs); err != nil {
		t.Fatalf("query result is not valid JSON: %v (result=%q)", err, res)
	}
	if len(programs) != 0 {
		t.Fatalf("expected empty program list, got %+v", programs)
	}
}

// TestProgramUpdateViaBus adds a program through the real dispatch path and
// verifies the durable-change signal fires and a follow-up query sees it.
func TestProgramUpdateViaBus(t *testing.T) {
	var calls int32c
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch,
		OnDurableChange: func() { calls.add() }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	ch.in <- event.New(TypeProgramUpdate, "tester", map[string]any{
		"op":           "add",
		"name":         "p1",
		"content_type": "playbook",
		"description":  "test playbook",
	})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if r, _ := e.Payload["result"].(string); r == "added program p1" {
				return true
			}
		}
		return false
	}, "request.completed for program.update add")
	waitCond(t, progTestTimeout, func() bool { return calls.get() > 0 }, "durable-change callback")

	// Query again: the added program must be visible.
	ch.in <- event.New(TypeProgramQuery, "tester", map[string]any{})
	waitCond(t, progTestTimeout, func() bool {
		return len(ch.eventsOf(event.TypeRequestCompleted)) >= 2
	}, "request.completed reply for second program.query")
	completed := ch.eventsOf(event.TypeRequestCompleted)[1]
	res, _ := completed.Payload["result"].(string)
	var programs []program.Program
	if err := json.Unmarshal([]byte(res), &programs); err != nil {
		t.Fatalf("query result is not valid JSON: %v (result=%q)", err, res)
	}
	if len(programs) != 1 || programs[0].Name != "p1" {
		t.Fatalf("query after add: programs=%+v", programs)
	}

	// Removing the same program succeeds and is visible.
	ch.in <- event.New(TypeProgramUpdate, "tester", map[string]any{"op": "remove", "name": "p1"})
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf(event.TypeRequestCompleted) {
			if r, _ := e.Payload["result"].(string); r == "removed program p1" {
				return true
			}
		}
		return false
	}, "request.completed for program.update remove")
}

// int32c is a tiny atomic counter for the durable-change hook.
type int32c struct {
	v atomic.Int32
}

func (c *int32c) add() { c.v.Add(1) }
func (c *int32c) get() int32 {
	return c.v.Load()
}

// scriptedProvider plays a fixed sequence of assistant messages: round 1 adds
// a program via program_update, round 2 lists them via program_query, round 3
// answers with text — the exact add→query flow, driven through real reasoning
// rounds so tool results land in the transcript.
type scriptedProvider struct {
	mu     sync.Mutex
	rounds int
}

func (p *scriptedProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, nil
}

func (p *scriptedProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	p.mu.Lock()
	p.rounds++
	n := p.rounds
	p.mu.Unlock()
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	switch n {
	case 1:
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "tool_calls",
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "program_update",
				ToolCallID:    "call_add",
				ToolArguments: `{"op":"add","name":"p1","content_type":"playbook","description":"d"}`}}})
	case 2:
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "tool_calls",
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "program_query",
				ToolCallID: "call_query", ToolArguments: `{}`}}})
	default:
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "done"}}})
	}
	return es, nil
}

func (p *scriptedProvider) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return nil, nil
}

// TestProgramToolCallAdvancesRound is the end-to-end regression for the reply
// path: the update's tool result must resolve and start the next round, and
// the query's serialized list must reach the transcript as the tool's text —
// resultOutcome reads the reply's "result" field when present, so a reply that
// carries its data under any other key would otherwise leave the LLM an EMPTY
// tool result (it would conclude the list is empty even though the query
// succeeded).
func TestProgramToolCallAdvancesRound(t *testing.T) {
	prov := &scriptedProvider{}
	ch := &selfLoopChannel{mockChannel: newMockChannel()}
	w := NewWorker(Config{ID: "r1", Provider: prov, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop() })

	// The self-ready announcement is queued first: tool dispatch resolves
	// capabilities through the discovery universe (HandleWorkerReady), which
	// in production is fed by the self-ready round-trip. The mock channel's
	// Send only records — it does not loop back into the watch loop — so the
	// test replays that announcement explicitly. FIFO ordering guarantees it
	// is processed before the input.
	ch.in <- event.New(event.TypeWorkerReady, "r1", map[string]any{
		"worker_id": "r1",
		"type":      "reason",
		"watch":     w.WatchEntries(true),
	})
	ch.in <- event.New(event.TypeWorkerInput, "tester", map[string]any{"text": "add then list your programs"})

	// Round 3 runs ("done") only if both tool results resolved.
	waitCond(t, progTestTimeout, func() bool {
		for _, e := range ch.eventsOf("reason.response") {
			if texts, ok := e.Payload["content"].([]any); ok && len(texts) > 0 {
				if s, _ := texts[0].(string); s == "done" {
					return true
				}
			}
		}
		return false
	}, "final reasoning round after update and query tool results")

	// The query's serialized list reached the transcript as tool-result text.
	waitCond(t, progTestTimeout, func() bool {
		for _, msg := range w.Messages() {
			for _, block := range msg.Content {
				if block.Type == llm.ContentText && strings.Contains(block.Text, `"name":"p1"`) {
					return true
				}
			}
		}
		return false
	}, "query result JSON in the transcript")
}
