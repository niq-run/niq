package reason

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/reason/transcript"
)

// TestPrepareReasoningBuildsRequest verifies prepareReasoning parks leftover
// tools, snapshots messages, and builds a completion request with the worker's
// ID instruction and tool set.
func TestPrepareReasoningBuildsRequest(t *testing.T) {
	w := newTestWorker(nil, nil)
	// Seed a trace and a pending tool call that should be parked at reasoning start.
	w.mu.Lock()
	w.currentTraceID = "trace1"
	w.requestTracker.Add("workspace", []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	})
	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}}}})
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
	if !w.requestTracker.Resolved() {
		t.Fatal("pending tool should be parked by prepareReasoning")
	}
}

// TestHandleToolCallsDispatches verifies tool calls are grouped by target and a
// tool.request is sent to each provider with the mapped (original) tool name.
func TestHandleToolCallsDispatches(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)
	w.tools["workspace__bash"] = worker.Tool{Name: "workspace__bash", Provider: "workspace"}
	w.toolNameMap["workspace__bash"] = "bash"

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "workspace__bash"},
	}
	w.mu.Lock()
	w.handleToolCalls(context.Background(), calls, "trace1")
	// handleToolCalls unlocks internally.

	// The tool.request should be directed to workspace with the mapped name.
	var name string
	for _, e := range ch.eventsOf(event.TypeToolRequest) {
		name, _ = e.Payload["name"].(string)
	}
	if name != "bash" {
		t.Fatalf("expected tool.request with mapped name %q, got %q", "bash", name)
	}
}

// TestHandleToolCallsNoMappingFails verifies a tool known to w.tools but absent
// from the toolNameMap is treated as a mismatch: it is NOT dispatched, an error
// tool_result lands in the transcript, and a tool_unavailable notice is
// broadcast (no silent trim-based guess).
func TestHandleToolCallsNoMappingFails(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)
	// Known to w.tools but deliberately missing from toolNameMap.
	w.tools["workspace__bash"] = worker.Tool{Name: "workspace__bash", Provider: "workspace"}

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "workspace__bash"},
	}
	w.mu.Lock()
	w.handleToolCalls(context.Background(), calls, "trace1")

	// Nothing dispatched.
	if n := len(ch.eventsOf(event.TypeToolRequest)); n != 0 {
		t.Fatalf("expected no tool.request (mapping missing), got %d", n)
	}

	// The mismatch surfaces as a tool_unavailable notice broadcast.
	var gotStop string
	for _, e := range ch.eventsOf(event.EventType("reason.response")) {
		if sr, _ := e.Payload["stop_reason"].(string); sr != "" {
			gotStop = sr
		}
	}
	if gotStop != "tool_unavailable" {
		t.Fatalf("expected a tool_unavailable notice, got stop_reason %q", gotStop)
	}

	// An error tool_result is recorded in the transcript.
	foundErr := false
	for _, m := range w.transcript.Render() {
		if m.ToolCallID == "c1" && m.IsError {
			foundErr = true
		}
	}
	if !foundErr {
		t.Fatal("transcript should carry an error tool_result for the unmapped tool")
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

	if len(ch.eventsOf(event.TypeToolRequest)) != 0 {
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

// TestMetaExtensionCallAndStripToolCalls moved to pkg/worker/reason:
// context.compress / context.rotate are now the default worker's toolkit, not
// the shared mechanism's.

// summarizeProvider routes Complete (summarizer) and CompleteStream (reasoning)
// to different fixed messages, recording the summarizer's system prompt.
type summarizeProvider struct {
	mu          sync.Mutex
	seenPrompt  string
	summarized  string
	chatMessage llm.Message
}

func (p *summarizeProvider) Complete(_ context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seenPrompt = req.Context.SystemPrompt
	return &llm.CompletionResponse{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "DIGEST(" + p.summarized + ")"}},
	}}, nil
}

func (p *summarizeProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(p.chatMessage)
	return es, nil
}

func (p *summarizeProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func (p *summarizeProvider) prompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seenPrompt
}

// TestSoftBudgetInjectsReminder verifies crossing the soft threshold appends
// exactly one system reminder per crossing, and no compression event is emitted.
func TestSoftBudgetInjectsReminder(t *testing.T) {
	prov := &summarizeProvider{summarized: "unused",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
			Usage:   &llm.Usage{InputTokens: 900, OutputTokens: 10},
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}}}
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: newTestChannel(),
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97})

	w.handleContextBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 900, OutputTokens: 10}})

	msgs := w.transcript.Render()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Content[0].Text, "91%") {
		t.Fatalf("expected one budget reminder, got %+v", msgs)
	}

	// Same side of the threshold: no duplicate reminder.
	w.handleContextBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 950, OutputTokens: 5}})
	if got := len(w.transcript.Render()); got != 1 {
		t.Fatalf("reminder should fire once per crossing, got %d messages", got)
	}
}

// TestHardBudgetEmitsMetaRequest verifies crossing the hard threshold routes
// a compression request through a worker.update meta request to itself (single audit
// path), not a direct mechanism edit — the worker shrinks its own transcript.
func TestHardBudgetEmitsMetaRequest(t *testing.T) {
	prov := &summarizeProvider{summarized: "hard-budget",
		chatMessage: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	ch := newTestChannel()
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: ch,
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97, KeepTail: 2})

	seed := func(n int) {
		for i := 0; i < n; i++ {
			w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{
				{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: string(rune('a' + i))}}},
			}})
		}
	}
	seed(6)

	w.handleContextBudget(context.Background(), llm.Message{Usage: &llm.Usage{InputTokens: 990, OutputTokens: 5}})

	var found bool
	for _, e := range ch.eventsOf(TypeContextCompress) {
		if e.Type == TypeContextCompress {
			found = true
		}
	}
	if !found {
		t.Fatalf("hard budget should emit a context.compress event, got %+v", ch.eventsOf(TypeContextCompress))
	}
}

// TestEnsureToolCallIDsSynthesizesMissing verifies the transcript invariant:
// every tool call entering the transcript carries a non-empty id, even when the
// model omitted it — otherwise the OpenAI-format pair (assistant tool_call +
// tool_result) is invalid and, because the transcript is persisted,
// unrecoverable.
func TestEnsureToolCallIDsSynthesizesMissing(t *testing.T) {
	w := newTestWorker(nil, nil)
	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.ContentToolCall, ToolName: "bash", ToolArguments: "{}"},
			{Type: llm.ContentToolCall, ToolCallID: "model-id", ToolName: "ls", ToolArguments: "{}"},
			{Type: llm.ContentToolCall, ToolName: "read", ToolArguments: "{}"},
		},
	}
	w.ensureToolCallIDs(msg)

	// The model-supplied id is preserved.
	if msg.Content[1].ToolCallID != "model-id" {
		t.Fatalf("expected model id preserved, got %q", msg.Content[1].ToolCallID)
	}
	if msg.Content[0].ToolCallID == "" || msg.Content[2].ToolCallID == "" {
		t.Fatalf("expected synthesized ids, got %q and %q", msg.Content[0].ToolCallID, msg.Content[2].ToolCallID)
	}
	if msg.Content[0].ToolCallID == msg.Content[2].ToolCallID {
		t.Fatalf("expected distinct ids, both %q", msg.Content[0].ToolCallID)
	}
}

// TestEnsureToolCallIDsUniqueAcrossRounds verifies ids do not repeat between
// calls: the tracker keeps pending calls across rounds, so a repeated id would
// mispair a result against the wrong call.
func TestEnsureToolCallIDsUniqueAcrossRounds(t *testing.T) {
	w := newTestWorker(nil, nil)
	var ids []string
	for i := 0; i < 3; i++ {
		msg := llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "bash"}},
		}
		w.ensureToolCallIDs(msg)
		ids = append(ids, msg.Content[0].ToolCallID)
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				t.Fatalf("id %q repeated across rounds %d and %d", ids[i], i, j)
			}
		}
	}
}
