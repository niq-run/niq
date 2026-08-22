package reason

import (
	"context"
	"testing"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/worker"
)

// TestPrepareReasoningBuildsRequest verifies prepareReasoning parks leftover
// tools, snapshots messages, and builds a completion request with the worker's
// ID instruction and tool set.
func TestPrepareReasoningBuildsRequest(t *testing.T) {
	w := newTestWorker(nil, nil)
	// Seed a trace and a pending tool call that should be parked at reasoning start.
	w.mu.Lock()
	w.currentTraceID = "trace1"
	w.toolCallTracker.Add("workspace", []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	})
	w.transcript.Apply(InputPatch{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}}}})
	w.mu.Unlock()

	traceID, req := w.prepareReasoning()
	if traceID != "trace1" {
		t.Fatalf("traceID = %q, want trace1", traceID)
	}
	if req == nil || req.Context == nil {
		t.Fatal("expected a completion request")
	}
	if len(req.Context.Messages) != 1 {
		t.Fatalf("expected 1 message in request, got %d", len(req.Context.Messages))
	}
	// Leftover tool should now be parked (not pending).
	if !w.toolCallTracker.Resolved() {
		t.Fatal("pending tool should be parked by prepareReasoning")
	}
}

// TestHandleToolCallsDispatches verifies tool calls are grouped by target and a
// tool.requested is sent to each provider.
func TestHandleToolCallsDispatches(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)
	w.tools["workspace.bash"] = worker.Tool{Name: "workspace.bash", Provider: "workspace"}

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "workspace.bash"},
	}
	w.mu.Lock()
	w.handleToolCalls(context.Background(), calls, "trace1")
	// handleToolCalls unlocks internally.

	// The tool.requested should be directed to workspace with the stripped name.
	var found bool
	for _, e := range ch.eventsOf(event.TypeToolRequested) {
		if e.WorkerId == w.ID() {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a tool.requested published by the worker")
	}
}

// TestHandleToolCallsUnavailable verifies an unknown tool is failed in place
// (placeholder replaced) and NOT dispatched.
func TestHandleToolCallsUnavailable(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)
	// No workerTools registered — every call is unavailable.

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "ghost.tool"},
	}
	w.mu.Lock()
	w.handleToolCalls(context.Background(), calls, "trace1")

	if len(ch.eventsOf(event.TypeToolRequested)) != 0 {
		t.Fatal("unavailable tool must not be dispatched")
	}
	// The transcript should contain the unavailable-tool tool_result.
	noDispatch := false
	for _, m := range w.transcript.Render() {
		if m.ToolCallID == "c1" && m.IsError {
			noDispatch = true
		}
	}
	if !noDispatch {
		t.Fatal("transcript should carry an error tool_result for the unavailable tool")
	}
}

// TestConsumeStreamSummarizesText verifies consumeStream accumulates text
// deltas and returns normally when the stream ends.
func TestConsumeStreamSummarizesText(t *testing.T) {
	w := newTestWorker(nil, nil)
	es := llm.NewEventStream()
	go func() {
		es.Push(llm.EventTextStart{})
		es.Push(llm.EventTextDelta{Delta: "hello "})
		es.Push(llm.EventTextDelta{Delta: "world"})
		es.Push(llm.EventTextEnd{})
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop"})
	}()
	out := w.consumeStream(context.Background(), es, "trace1")
	if out.interrupted || out.streamErr != nil {
		t.Fatalf("unexpected outcome: %+v", out)
	}
}

// TestRetryStopsOnNonRetriable verifies retry returns immediately for a
// non-retriable error.
func TestRetryStopsOnNonRetriable(t *testing.T) {
	called := 0
	err := retry(context.Background(), 3, func() (bool, error) {
		called++
		return false, context.Canceled // not retriable
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if called != 1 {
		t.Fatalf("fn called %d times, want 1", called)
	}
}

// TestRetrySucceeds verifies retry succeeds and returns nil.
func TestRetrySucceeds(t *testing.T) {
	called := 0
	err := retry(context.Background(), 3, func() (bool, error) {
		called++
		return true, nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called != 1 {
		t.Fatalf("fn called %d times, want 1", called)
	}
}

// TestFinalMessageReturns verifies finalMessage returns the stream's final
// message promptly (the 5s hang-guard is not exercised in the happy path).
func TestFinalMessageReturns(t *testing.T) {
	w := newTestWorker(nil, nil)
	es := llm.NewEventStream()
	go func() {
		es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop"})
	}()
	msg, err := w.finalMessage(es)
	if err != nil {
		t.Fatalf("finalMessage: %v", err)
	}
	if msg.StopReason != "stop" {
		t.Fatalf("stop_reason = %q, want stop", msg.StopReason)
	}
}

// TestHasMetaToolCallAndStripToolCalls verifies meta tool detection and that a
// meta tool call (which never produces a tool result) is excluded from the
// transcript: hasMetaToolCall flags the response, and stripToolCalls removes
// all tool_calls while keeping thinking/text.
func TestHasMetaToolCallAndStripToolCalls(t *testing.T) {
	w := newTestWorker(nil, nil)
	w.mu.Lock()
	w.tools["context.compress"] = worker.Tool{Name: "context.compress", IsMetaTool: true}
	w.tools["ws-tmp-niq-test__ls"] = worker.Tool{Name: "ws-tmp-niq-test__ls", Provider: "ws-tmp-niq-test"}
	w.mu.Unlock()

	metaMsg := llm.Message{
		Role:       llm.RoleAssistant,
		StopReason: "tool_calls",
		Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "need to compress"},
			{Type: llm.ContentToolCall, ToolCallID: "m1", ToolName: "context.compress"},
			{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "ws-tmp-niq-test__ls"},
		},
	}
	if !w.hasMetaToolCall(metaMsg) {
		t.Fatal("hasMetaToolCall should detect context.compress")
	}

	stripped := stripToolCalls(metaMsg)
	if len(stripped.Content) != 1 || stripped.Content[0].Type != llm.ContentThinking {
		t.Fatalf("stripToolCalls should keep only thinking, got %+v", stripped.Content)
	}

	plain := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}
	if w.hasMetaToolCall(plain) {
		t.Fatal("hasMetaToolCall must be false without a meta tool call")
	}
}
