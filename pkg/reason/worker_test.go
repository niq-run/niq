package reason

import (
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/reason/transcript"
)

// TestInputDefaultTriggersReasoning verifies a default-mode input produces a
// completed reasoning round (a reason.end event).
func TestInputDefaultTriggersReasoning(t *testing.T) {
	prov := &staticProvider{msg: llm.Message{
		Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}},
	}}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}

// TestInputAppendDoesNotInterruptReasoning verifies an append-mode input does
// NOT cancel an in-flight reasoning call (unlike default mode, which interrupts).
func TestInputAppendDoesNotInterruptReasoning(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	// Start a reasoning round and hold it in flight.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")

	// append-mode input must not interrupt the in-flight call.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "note", "input_mode": "append"})
	time.Sleep(100 * time.Millisecond)
	if ch.hasInterrupted() {
		t.Fatal("append-mode input must not interrupt in-flight reasoning")
	}

	// Release the provider; the round completes normally.
	close(prov.release)
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}

// seedPendingCall plants a dispatched-but-unresolved tool call: a tracker
// entry plus its [pending] transcript placeholder — the state a snapshot taken
// mid-tool-round carries.
func seedPendingCall(w *BaseReasonWorker, callID, name, target string) {
	call := llm.ContentBlock{Type: llm.ContentToolCall, ToolCallID: callID, ToolName: name}
	w.requestTracker.Add(target, []llm.ContentBlock{call})
	w.transcript.Apply(transcript.ToolPlaceholdersPatch{Calls: []llm.ContentBlock{call}})
}

// TestSnapshotRestoreParksPendingRequests verifies a pending tool call survives
// a snapshot: the restored worker parks it (a wait whose target restarted can
// never end, and Resolved() gates schedule-mode input), rewrites the
// [pending] placeholder with the restart explanation, and still matches a
// result arriving afterwards as a late result instead of dropping it.
func TestSnapshotRestoreParksPendingRequests(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	// No w.mu here: seedPendingCall's pieces lock themselves, and Snapshot()
	// takes w.mu — holding it across the call would self-deadlock.
	seedPendingCall(w, "c1", "bash", "workspace")
	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	fresh := newTestWorker(&staticProvider{}, nil)
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if !fresh.requestTracker.Resolved() {
		t.Fatal("restored pending call must be parked (resolved), not awaited")
	}
	msgs := fresh.transcript.Render()
	if len(msgs) != 1 || msgs[0].Role != llm.RoleToolResult {
		t.Fatalf("expected the placeholder to survive restore, got %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content[0].Text, "did not survive the restart") {
		t.Fatalf("placeholder should carry the restart explanation, got %q", msgs[0].Content[0].Text)
	}

	// The result still arrives afterwards: matched as a late result.
	late := event.New(event.TypeRequestCompleted, "workspace", map[string]any{"result": "late-out"})
	late.RequestId = "c1"
	fresh.handleToolResult(late)

	msgs = fresh.transcript.Render()
	if len(msgs) != 2 {
		t.Fatalf("late result should append one message, got %d", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleUser {
		t.Fatalf("late result should be a user message, got role %q", last.Role)
	}
	if !strings.Contains(last.Content[0].Text, "late-out") {
		t.Fatalf("late message should carry the outcome, got %+v", last.Content)
	}
}

// TestSnapshotRestoreToolCallSeq verifies the synthesized tool-call id counter
// crosses a snapshot: its uniqueness domain is the whole restored transcript,
// so restarting from 0 would re-mint an id already in history and a result
// would patch that old message instead of the new call's placeholder.
func TestSnapshotRestoreToolCallSeq(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.ensureToolCallIDs(llm.Message{Content: []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolName: "a"},
		{Type: llm.ContentToolCall, ToolName: "b"},
	}}) // mints call_1, call_2
	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	fresh := newTestWorker(&staticProvider{}, nil)
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	msg := llm.Message{Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "c"}}}
	fresh.ensureToolCallIDs(msg)
	if got := msg.Content[0].ToolCallID; got != "call_3" {
		t.Fatalf("first synthesized id after restore = %q, want call_3", got)
	}
}

// TestRestoreSnapshotWithoutTrackerFields verifies field-absent older snapshots
// restore cleanly: an empty tracker and a zero id counter.
func TestRestoreSnapshotWithoutTrackerFields(t *testing.T) {
	fresh := newTestWorker(&staticProvider{}, nil)
	if err := fresh.Restore([]byte(`{"transcript":null}`)); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !fresh.requestTracker.Resolved() {
		t.Fatal("tracker should be empty after restoring an old snapshot")
	}
	msg := llm.Message{Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "a"}}}
	fresh.ensureToolCallIDs(msg)
	if got := msg.Content[0].ToolCallID; got != "call_1" {
		t.Fatalf("first synthesized id = %q, want call_1", got)
	}
}
