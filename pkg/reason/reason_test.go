package reason

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
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

// TestHandleToolCallsDispatches verifies tool calls are grouped by target and
// dispatched as the capability's own event type to the declaring worker.
func TestHandleToolCallsDispatches(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)

	// A peer worker announces a "bash" tool via the watch channel.
	w.HandleWorkerReady(event.New(event.TypeWorkerReady, "workspace", map[string]any{
		"worker_id": "workspace",
		"watch":     []map[string]any{{"event": "bash", "desc": "Run a command"}},
	}))

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "workspace__bash"},
	}
	w.mu.Lock()
	w.handleToolCalls(context.Background(), calls, "trace1")
	// handleToolCalls unlocks internally.

	// The call is dispatched to workspace as its own event type "bash".
	found := false
	for _, e := range ch.eventsOf("bash") {
		if e.RequestId == "c1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a bash event to workspace with RequestId c1")
	}
}

// TestHandleToolCallsUnknownFails verifies an unknown tool (not discovered) is
// NOT dispatched, an error tool_result lands in the transcript, and a
// tool_unavailable notice is broadcast.
func TestHandleToolCallsUnknownFails(t *testing.T) {
	ch := newTestChannel()
	// No worker announces "bash", so it is unknown. The all-unavailable round
	// now schedules a follow-up round; give it a provider so that round
	// terminates cleanly (text-only) instead of panicking on a nil provider.
	prov := &staticProvider{msg: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	w := newTestWorker(prov, ch)

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "workspace__bash"},
	}
	w.mu.Lock()
	// finishReasoning inserts the placeholders before dispatching; mirror it so
	// the error ToolResultPatch has a placeholder to replace in place.
	w.transcript.Apply(transcript.ToolPlaceholdersPatch{Calls: calls})
	w.handleToolCalls(context.Background(), calls, "trace1")

	// Nothing dispatched.
	if n := len(ch.eventsOf("bash")); n != 0 {
		t.Fatalf("expected no dispatch (tool unknown), got %d", n)
	}

	// The failure surfaces as a tool_unavailable notice broadcast.
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
		t.Fatal("transcript should carry an error tool_result for the unknown tool")
	}
}

// TestHandleToolCallsUnavailable verifies an unknown tool is failed in place
// (placeholder replaced) and NOT dispatched.
func TestHandleToolCallsUnavailable(t *testing.T) {
	ch := newTestChannel()
	// The all-unavailable round now schedules a follow-up round; give it a
	// provider so that round terminates cleanly (text-only).
	prov := &staticProvider{msg: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}
	w := newTestWorker(prov, ch)
	// No workerTools registered — every call is unavailable.

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "ghost.tool"},
	}
	w.mu.Lock()
	// finishReasoning inserts the placeholders before dispatching; mirror it so
	// the error ToolResultPatch has a placeholder to replace in place.
	w.transcript.Apply(transcript.ToolPlaceholdersPatch{Calls: calls})
	w.handleToolCalls(context.Background(), calls, "trace1")

	if len(ch.eventsOf("bash")) != 0 {
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

// TestUnavailableToolSchedulesFollowUp verifies that a round whose tool calls
// are ALL unavailable does not stall: because nothing was dispatched, no tool
// result event will ever arrive to set needReason, so handleToolCalls must
// self-schedule a follow-up round — otherwise the LLM never gets to react to
// the "tool unavailable" error. The round is observed via blockingProvider's
// started signal (fired when the follow-up's CompleteStream is called).
//
//  1. round ends with only unavailable call(s) -> follow-up round launches.
func TestUnavailableToolSchedulesFollowUp(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	ch := newTestChannel()
	w := newTestWorker(prov, ch)

	calls := []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "ghost.tool"},
	}
	w.mu.Lock()
	w.transcript.Apply(transcript.ToolPlaceholdersPatch{Calls: calls})
	w.handleToolCalls(context.Background(), calls, "trace1")
	// handleToolCalls unlocks internally; the fix sets needReason and tryReason
	// spawns the follow-up round on a goroutine, which calls the provider.

	// The follow-up round must start despite nothing having been dispatched.
	waitCond(t, testTimeout, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "follow-up reasoning round starts after an all-unavailable round")

	// Release the blocked round so it finishes cleanly (text-only); the worker
	// then goes idle with no further rounds scheduled.
	close(prov.release)
	waitCond(t, testTimeout, func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return !w.isReasoning
	}, "follow-up round completes")
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

// mixedRoundProvider returns a message that carries BOTH text and a tool call
// in one round, so the leading text must be published as a durable
// reason.response before the tool call is dispatched.
type mixedRoundProvider struct {
	msg llm.Message
}

func (p *mixedRoundProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Message: p.msg}, nil
}

func (p *mixedRoundProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(p.msg)
	return es, nil
}

func (p *mixedRoundProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// TestUsageMeta verifies the reason.response meta payload renders the round's
// context usage (and stays nil when the provider reported none).
func TestUsageMeta(t *testing.T) {
	cacheRead := 42
	cacheCreate := 7

	// Full usage including cache fields and the context window.
	m := usageMeta(llm.Message{Usage: &llm.Usage{
		InputTokens:         100,
		OutputTokens:        25,
		TotalTokens:         125,
		CacheReadTokens:     &cacheRead,
		CacheCreationTokens: &cacheCreate,
	}}, 1000)
	if m == nil {
		t.Fatal("usageMeta returned nil for a message with usage")
	}
	if m["input_tokens"] != 100 || m["output_tokens"] != 25 || m["total_tokens"] != 125 {
		t.Fatalf("unexpected token counts: %+v", m)
	}
	if m["context_window"] != 1000 {
		t.Fatalf("unexpected context_window: %+v", m)
	}
	if m["cache_read_tokens"] != 42 || m["cache_creation_tokens"] != 7 {
		t.Fatalf("unexpected cache tokens: %+v", m)
	}

	// Zero/unknown context window → field omitted.
	if m := usageMeta(llm.Message{Usage: &llm.Usage{InputTokens: 1}}, 0); m["context_window"] != nil {
		t.Fatalf("context_window should be omitted when unknown, got %+v", m["context_window"])
	}

	// No usage → nil meta (field omitted).
	if m := usageMeta(llm.Message{}, 1000); m != nil {
		t.Fatalf("usageMeta should be nil without usage, got %+v", m)
	}
}

// TestMixedRoundPublishesLeadingText verifies that when a single reasoning round
// contains both text and a tool call, the leading text is published as a durable
// reason.response (so it survives replay) rather than only existing as transient
// text_delta events that are never persisted.
func TestMixedRoundPublishesLeadingText(t *testing.T) {
	const lead = "leading note that must be persisted"
	prov := &mixedRoundProvider{msg: llm.Message{
		Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: lead},
			{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "context_compress", ToolArguments: "{}"},
		},
	}}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "go", "input_mode": "default"})

	waitCond(t, 2*time.Second, func() bool {
		for _, evt := range ch.eventsOf("reason.response") {
			payload := evt.Payload["content"]
			arr, ok := payload.([]any)
			if !ok || len(arr) == 0 {
				continue
			}
			if s, ok := arr[0].(string); ok && s == lead {
				return true
			}
		}
		return false
	}, "mixed round to publish its leading text as reason.response")

	// Each completed LLM call ends one reasoning round; with the worker parked on
	// the pending tool result there will be exactly one reason.end so far.
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) >= 1
	}, "tool-call round to emit reason.end")
}

// TestTranscriptEditCallAndStripToolCalls moved to pkg/worker/reason:
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

// TestSoftBudgetReminderStaysAfterToolResult verifies that for a round ending
// in a tool call, the inline soft-budget reminder lands AFTER the assistant
// tool_call and its (pending→resolved) tool result — it is the round's last
// transcript write because finishReasoning inserts the tool placeholders before
// handleContextBudget. The assistant→tool pairing is never broken.
func TestSoftBudgetReminderStaysAfterToolResult(t *testing.T) {
	prov := &summarizeProvider{summarized: "unused"}
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: prov, Bus: newTestChannel(),
		ContextWindow: 1000, BudgetSoft: 0.85, BudgetHard: 0.97})

	// Mirror finishReasoning's order: assistant tool_call, then the placeholders
	// (inserted before the budget check), then handleContextBudget appends the
	// reminder as the round's LAST transcript write.
	w.transcript.Apply(transcript.AssistantOutputPatch{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"}},
	}})
	w.transcript.Apply(transcript.ToolPlaceholdersPatch{Calls: []llm.ContentBlock{{
		Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"}}})
	w.handleContextBudget(context.Background(), llm.Message{
		Usage:   &llm.Usage{InputTokens: 900, OutputTokens: 10},
		Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"}},
	})

	// Before the tool result resolves, the transcript must be assistant→tool→user
	// (the reminder is the trailing user message, after the placeholder).
	msgs := w.transcript.Render()
	if len(msgs) != 3 {
		t.Fatalf("expected assistant→tool→user, got %d messages", len(msgs))
	}
	if msgs[0].Role != llm.RoleAssistant || msgs[1].Role != llm.RoleToolResult {
		t.Fatalf("unexpected leading roles: %s, %s", msgs[0].Role, msgs[1].Role)
	}
	if last := msgs[2]; last.Role != llm.RoleUser || !strings.Contains(last.Content[0].Text, "91%") {
		t.Fatalf("reminder must be the trailing user message, got %+v", last)
	}

	// Once the real tool result replaces the placeholder (in place), the order
	// assistant→tool(result)→user still holds — the pairing is intact.
	w.transcript.Apply(transcript.ToolResultPatch{CallID: "c1", Name: "bash", Text: "ok"})
	msgs = w.transcript.Render()
	if len(msgs) != 3 {
		t.Fatalf("after result, expected assistant→tool→user, got %d messages", len(msgs))
	}
	if msgs[1].Role != llm.RoleToolResult || !strings.Contains(msgs[1].Content[0].Text, "ok") {
		t.Fatalf("tool result should have replaced the placeholder, got %+v", msgs[1])
	}
	if msgs[2].Role != llm.RoleUser {
		t.Fatalf("reminder should still trail the tool result, got %s", msgs[2].Role)
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
