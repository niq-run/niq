package reason

import (
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/reason/requesttracker"
)

// Tests for process.go's event-to-input translation: DefaultConverter,
// resultOutcome, and convertEvent's source-filtered converter selection.
// (EventPattern.Matches semantics are tested in core/event.)

// TestDefaultConverterText verifies a payload with a "text" field produces a
// user message leading with that text (without repeating the payload).
func TestDefaultConverterText(t *testing.T) {
	evt := event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello"})
	msgs := DefaultConverter(evt)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Role != llm.RoleUser {
		t.Fatalf("role = %q, want user", m.Role)
	}
	if len(m.Content) != 1 || m.Content[0].Type != llm.ContentText {
		t.Fatalf("expected one text block, got %+v", m.Content)
	}
	if m.Content[0].Text != "hello\n\n[Event: worker.input from hiw]" {
		t.Fatalf("unexpected text: %q", m.Content[0].Text)
	}
}

// TestDefaultConverterNoText verifies an event without a text field still
// becomes a user message with the event metadata.
func TestDefaultConverterNoText(t *testing.T) {
	evt := event.New(event.TypeWorkerReady, "workspace", map[string]any{"worker_id": "workspace"})
	msgs := DefaultConverter(evt)
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser {
		t.Fatalf("role = %q, want user", msgs[0].Role)
	}
}

// TestResultOutcome verifies resultOutcome extracts text and the error flag
// from each tool-result event type.
func TestResultOutcome(t *testing.T) {
	cases := []struct {
		name    string
		evtType event.EventType
		payload map[string]any
		want    string
		isError bool
	}{
		{"completed", event.TypeRequestCompleted, map[string]any{"result": "ok"}, "ok", false},
		{"failed", event.TypeRequestFailed, map[string]any{"error": "boom"}, "Tool call failed: boom", true},
		{"rejected", event.TypeRequestRejected, map[string]any{"reason": "no"}, "Tool call rejected: no", true},
		{"missing", event.TypeRequestCompleted, map[string]any{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evt := event.New(c.evtType, "workspace", c.payload)
			text, isErr := resultOutcome(evt)
			if text != c.want || isErr != c.isError {
				t.Fatalf("resultOutcome = (%q, %v), want (%q, %v)", text, isErr, c.want, c.isError)
			}
		})
	}
}

// Placeholder-family behavior (resultMessage/parkReason/unavailable/
// late-result, previously tested here) now lives in the builder package.

// TestEventPatternSourceFilter verifies source-filtered matching end to end
// through convertEvent.
func TestEventPatternSourceFilter(t *testing.T) {
	var converted bool
	marker := func(evt event.Event) []llm.Message {
		converted = true
		return []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "marker"}}}}
	}
	w := &BaseReasonWorker{
		eventConverters: []EventConverter{
			{Pattern: event.EventPattern{Type: "pr.ready", SourceID: "gh"}, Converter: marker},
		},
	}

	// Matching source: marker converter selected.
	w.convertEvent(event.Event{Type: "pr.ready", WorkerId: "gh"})
	if !converted {
		t.Errorf("source match: expected marker converter to run")
	}

	// Non-matching source: marker converter NOT selected (falls back).
	converted = false
	w.convertEvent(event.Event{Type: "pr.ready", WorkerId: "gitlab"})
	if converted {
		t.Errorf("source miss: marker converter should not run")
	}
}

// TestAbortParksTools verifies an abort event parks pending tools and records
// the abort in the
func TestAbortParksTools(t *testing.T) {
	w, _, _ := startWorker(t, &staticProvider{})
	// Simulate a pending tool call.
	w.mu.Lock()
	w.requestTracker.Add("workspace", []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	})
	w.mu.Unlock()

	w.handleAbort(event.New(event.TypeWorkerAbort, "swarm", map[string]any{}))
	if !w.requestTracker.Resolved() {
		t.Fatal("abort should park all pending tools")
	}
}

// TestLateToolResult verifies a result arriving after a call was parked is
// appended as a contextualized late message, not a duplicate tool message.
func TestLateToolResult(t *testing.T) {
	w, _, _ := startWorker(t, &staticProvider{})
	w.mu.Lock()
	w.needReason = false
	w.requestTracker.Add("workspace", []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	})
	w.requestTracker.ParkAll(requesttracker.PreemptCauseTimeout)
	w.mu.Unlock()

	late := event.New(event.TypeRequestCompleted, "workspace", map[string]any{
		"name": "bash", "result": "late-out",
	})
	late.RequestId = "c1"
	before := len(w.transcript.Render())
	w.handleToolResult(late)

	msgs := w.transcript.Render()
	if len(msgs) != before+1 {
		t.Fatalf("late result should append one message, got %d->%d", before, len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("late result should be a user message, got role %q", last.Role)
	}
	if len(last.Content) == 0 || !strings.Contains(last.Content[0].Text, "late-out") {
		t.Fatalf("late message should carry the outcome, got %+v", last.Content)
	}
}

// TestInputAppendIsGentle verifies the "append" input_mode (level 2) does
// not interrupt an in-flight reasoning call, but schedules the next round and
// records the cause for parking.
func TestInputAppendIsGentle(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	w, ch, _ := startWorker(t, prov)

	// Hold a reasoning round in flight.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, testTimeout, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")

	// An append-mode input must not interrupt the in-flight call.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "note", "input_mode": "append"})
	time.Sleep(50 * time.Millisecond)
	if ch.hasInterrupted() {
		t.Fatal("append-mode input must not interrupt in-flight reasoning")
	}

	// The append input records the cause for the next round's parking.
	waitCond(t, testTimeout, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.immediateReasoningCause == requesttracker.PreemptCauseInput
	}, "append input to record cause")
	w.mu.Lock()
	cause := w.immediateReasoningCause
	w.mu.Unlock()
	if cause != requesttracker.PreemptCauseInput {
		t.Fatalf("append input should set immediateReasoningCause, got %q", cause)
	}

	close(prov.release)
	waitCond(t, testTimeout, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}
