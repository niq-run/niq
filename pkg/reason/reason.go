// LLM reasoning round.
//
// reason: one-shot LLM call → text response or tool calls with placeholders
package reason

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/54c1/niq/core/event"
	llm "github.com/54c1/niq/core/llm"
)

func (w *BaseReasonWorker) reason(ctx context.Context) {
	// Phase 1: Setup (lock). Park leftover tools, snapshot state, build request.
	traceID, req := w.prepareReasoning()

	// Phase 2: Streaming LLM call.
	w.broadcastReasonStart(traceID)

	reasonCtx, cancel := context.WithCancel(ctx)
	w.mu.Lock()
	w.cancelReason = cancel
	w.mu.Unlock()

	stream, err := w.openStream(reasonCtx, req, traceID)
	if err != nil {
		w.handleStreamStartError(ctx, reasonCtx, traceID, err)
		return
	}

	outcome := w.consumeStream(reasonCtx, stream, traceID)

	// Phase 3: State update (lock).
	w.mu.Lock()
	w.cancelReason = nil

	if outcome.interrupted {
		w.finishInterrupted(ctx, traceID, outcome)
		return
	}
	if outcome.streamErr != nil {
		w.broadcastErrorAndEnd(ctx, traceID, outcome.streamErr)
		return
	}

	finalMsg, err := w.finalMessage(stream)
	if err != nil {
		w.broadcastErrorAndEnd(ctx, traceID, err)
		return
	}
	w.finishReasoning(ctx, traceID, finalMsg)
}

// prepareReasoning snapshots the reasoning state under lock and builds the
// completion request: parks leftover tools, captures the trace and current
// messages, and assembles the request with the current tool set. Returns
// after releasing the lock.
func (w *BaseReasonWorker) prepareReasoning() (traceID string, req *llm.CompletionRequest) {
	w.mu.Lock()
	w.isReasoning = true
	w.activeTimeout = "" // each reasoning starts with no active timeout timer
	w.activeTimeoutProvider = ""

	// Park any pending tools from the previous reasoning before taking the
	// snapshot, so the LLM sees the parked context. The cause is taken
	// from immediateReasoningCause if set, otherwise falls back to Input.
	cause := w.immediateReasoningCause
	if cause == "" {
		cause = PreemptCauseInput
	}
	w.immediateReasoningCause = ""
	w.parkPending(cause)

	traceID = w.currentTraceID
	tools := w.llmToolDefs()
	c := &llm.Context{
		SystemPrompt:    w.buildInstruction(),
		Messages:        w.transcript.Render(),
		Tools:           tools,
		ReasoningEffort: w.reasoningEffort,
	}
	req = &llm.CompletionRequest{Context: c}
	if len(tools) > 0 {
		req.ToolChoice = llm.ToolChoiceAuto
	}
	w.mu.Unlock()

	return traceID, req
}

// openStream starts the LLM stream, retrying on transient errors. Error
// policy (what to retry, and what recovery to schedule for a terminal error)
// lives in llmerror.go.
func (w *BaseReasonWorker) openStream(reasonCtx context.Context, req *llm.CompletionRequest, traceID string) (*llm.EventStream, error) {
	var stream *llm.EventStream
	err := retry(reasonCtx, 5, func() (bool, error) {
		s, callErr := w.llmProvider.CompleteStream(reasonCtx, req)
		if callErr == nil {
			stream = s
			w.resetRateLimitBackoff()
			return false, nil
		}
		return w.decideLLMError(reasonCtx, traceID, callErr)
	})
	return stream, err
}

// renderTranscriptLocked returns the current transcript for a caller that is
// NOT holding w.mu. It takes the lock itself: Render() is only safe under the
// mutex (transcripts are passive; the caller serializes access), so callers
// outside the lock must go through this wrapper rather than Render() directly.
func (w *BaseReasonWorker) renderTranscriptLocked() []llm.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.transcript.Render()
}

// retry executes fn up to maxRetries times with exponential backoff.
// fn returns (shouldRetry, error). If shouldRetry is true and error is
// non-nil, retries after backoff. Otherwise stops immediately.
func retry(ctx context.Context, maxRetries int, fn func() (bool, error)) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		shouldRetry, err := fn()
		if err == nil {
			return nil
		}
		if !shouldRetry || attempt == maxRetries {
			return err
		}
		backoff := time.Duration(1<<uint(attempt)) * time.Second
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// streamOutcome summarizes the result of consuming an LLM stream.
type streamOutcome struct {
	interrupted     bool
	streamErr       error
	partialThinking string
	partialText     string
}

// consumeStream reads deltas off the stream, batching them into periodic
// delta events, until the stream ends or the reasoning is interrupted. On
// interruption it drains the stream and preserves any partial content
// into the transcript so the next reasoning can continue from it.
func (w *BaseReasonWorker) consumeStream(reasonCtx context.Context, stream *llm.EventStream, traceID string) streamOutcome {
	var (
		thinkingBuf     strings.Builder
		textBuf         strings.Builder
		streamErr       error
		partialThinking string
		partialText     string
	)

	const batchInterval = 5 * time.Second
	ticker := time.NewTicker(batchInterval)
	defer ticker.Stop()

	flushBatches := func() {
		if thinkingBuf.Len() > 0 {
			w.broadcastThinkingDelta(thinkingBuf.String(), traceID)
			thinkingBuf.Reset()
		}
		if textBuf.Len() > 0 {
			w.broadcastTextDelta(textBuf.String(), traceID)
			textBuf.Reset()
		}
	}

	streamDone := false
	interrupted := false

	for !streamDone {
		select {
		case <-reasonCtx.Done():
			// Interrupted: abort or new input preempted the reasoning. Save
			// accumulated content before flushing (flush resets buffers).
			interrupted = true
			partialThinking = thinkingBuf.String()
			partialText = textBuf.String()
			drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
			stream.Drain(drainCtx)
			drainCancel()
			flushBatches()
			// Preserve partial content to the working context.
			if partialThinking != "" || partialText != "" {
				var blocks []llm.ContentBlock
				if partialThinking != "" {
					blocks = append(blocks, llm.ContentBlock{Type: llm.ContentThinking, Text: partialThinking})
				}
				if partialText != "" {
					blocks = append(blocks, llm.ContentBlock{Type: llm.ContentText, Text: partialText})
				}
				w.mu.Lock()
				w.transcript.Apply(PartialOutputPatch{Message: llm.Message{
					Role:       llm.RoleAssistant,
					Content:    blocks,
					StopReason: "interrupted",
				}})
				w.mu.Unlock()
			}
			streamDone = true

		case <-ticker.C:
			// 5s elapsed: flush accumulated deltas as batch events.
			flushBatches()

		case evt, ok := <-stream.C():
			if !ok {
				// Stream exhausted: flush remaining content, then get final message.
				flushBatches()
				streamDone = true
				break
			}
			switch e := evt.(type) {
			case llm.EventThinkingDelta:
				// Thinking delta: accumulate for batch flush.
				thinkingBuf.WriteString(e.Delta)
			case llm.EventTextDelta:
				// Text delta: accumulate for batch flush.
				textBuf.WriteString(e.Delta)
			case llm.EventError:
				// Stream error: flush what we have, surface in Phase 3.
				streamErr = e.Err
				flushBatches()
				streamDone = true
			}
		}
	}

	return streamOutcome{
		interrupted:     interrupted,
		streamErr:       streamErr,
		partialThinking: partialThinking,
		partialText:     partialText,
	}
}

// finalMessage retrieves the final message from a completed stream.
//
// The 5s timeout is a hang-guard, not a normal path: End/Abort always populate
// the stream's result channel before closing its events channel, so by the time
// the stream is exhausted Result returns immediately. The bound only matters if
// a provider ends a stream without delivering a final message — it turns that
// into an error instead of hanging the reasoning goroutine forever.
func (w *BaseReasonWorker) finalMessage(stream *llm.EventStream) (llm.Message, error) {
	resultCtx, resultCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer resultCancel()
	return stream.Result(resultCtx)
}

// handleStreamStartError handles a failure to open the LLM stream. If the
// request was interrupted (abort or new input preempted the reasoning), it
// broadcasts the interrupted lifecycle quietly; otherwise it surfaces the error.
func (w *BaseReasonWorker) handleStreamStartError(ctx context.Context, reasonCtx context.Context, traceID string, err error) {
	w.mu.Lock()
	w.cancelReason = nil

	if errors.Is(err, errRateLimitBackoff) {
		// A retry reminder is already scheduled; end the round quietly. The
		// timer.reminder triggers the next attempt — no tryReason here.
		w.broadcastReasonEnd(traceID, StopReasonRateLimited)
		w.isReasoning = false
		w.mu.Unlock()
		return
	}

	if reasonCtx.Err() != nil {
		log.Printf("[reason %s] reasoning interrupted", w.ID())
		w.interruptReason = "" // consumed; don't let it leak into a later round
		w.broadcastReasonEnd(traceID, StopReasonInterrupted)
		w.isReasoning = false
		w.mu.Unlock()
		w.tryReason(ctx)
		return
	}

	w.broadcastErrorAndEnd(ctx, traceID, err)
}

// finishInterrupted broadcasts the interrupted lifecycle for reasoning that
// was cancelled mid-stream. The partial content is preserved in the
//
//	Expects w.mu to be held; unlocks it and calls tryReason before
//
// returning.
func (w *BaseReasonWorker) finishInterrupted(ctx context.Context, traceID string, out streamOutcome) {
	cause := w.interruptReason
	w.interruptReason = "" // consumed; don't let it leak into a later round
	if cause == "" {
		cause = "unknown"
	}
	preserved := out.partialThinking + out.partialText
	log.Printf("[reason %s] reasoning interrupted (cause=%s, preserved=%d chars)", w.ID(), cause, len(preserved))
	evt := event.New("reason.interrupted", w.ID(), map[string]any{
		"reason":          string(cause),
		"preserved_chars": len(preserved),
		"preserved_text":  preserved,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
	w.broadcastReasonEnd(traceID, StopReasonInterrupted)
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}

// finishReasoning completes a successful reasoning: records the final
// message, publishes thinking blocks, and either dispatches tool calls or
// broadcasts the text response. Expects w.mu to be held; unlocks it and calls
// tryReason before returning.
func (w *BaseReasonWorker) finishReasoning(ctx context.Context, traceID string, finalMsg llm.Message) {
	log.Printf("[reason %s] LLM response: stop_reason=%s, content_blocks=%d",
		w.ID(), finalMsg.StopReason, len(finalMsg.Content))

	// If the response contains a meta tool call, ALL tool calls are discarded:
	// a meta operation edits the transcript itself, so its call never enters it.
	// The applied assistant message carries only thinking/text; handleToolCalls
	// still sees the calls and emits worker.update.
	appliedMsg := finalMsg
	if _, isMeta := w.metaCapabilityCall(finalMsg); isMeta {
		appliedMsg = stripToolCalls(finalMsg)
	}
	// Guarantee every tool call carries a stable id before it enters the
	// transcript. Some models omit the tool_call id; an empty id makes the
	// OpenAI-format pair (assistant tool_call + its tool_result) invalid, and
	// because the transcript is persisted such a message is unrecoverable —
	// every later LLM call 400s. Synthesizing here (not in the provider) keeps
	// the guarantee provider-agnostic and ensures the matching tool_result
	// reuses the same id via the tracker.
	w.ensureToolCallIDs(finalMsg)
	w.transcript.Apply(AssistantOutputPatch{Message: appliedMsg})

	// Budget check: record the round's usage and act on thresholds
	// (soft: remind, hard: schedule compaction). Expects w.mu held.
	w.handleBudget(ctx, finalMsg)

	// Collect tool calls and thinking blocks from the response.
	var toolCalls []llm.ContentBlock
	var thinkingBlocks []llm.ContentBlock
	for _, block := range finalMsg.Content {
		switch block.Type {
		case llm.ContentToolCall:
			toolCalls = append(toolCalls, block)
		case llm.ContentThinking:
			thinkingBlocks = append(thinkingBlocks, block)
		}
	}
	if len(thinkingBlocks) > 0 {
		log.Printf("[reason %s] publishing %d thinking block(s)", w.ID(), len(thinkingBlocks))
		w.broadcastThinking(thinkingBlocks, traceID)
	} else {
		log.Printf("[reason %s] no thinking blocks in response (content_blocks=%d)", w.ID(), len(finalMsg.Content))
	}

	if len(toolCalls) > 0 {
		w.handleToolCalls(ctx, toolCalls, traceID)
		return
	}

	// Text-only response: publish content, then signal reasoning end.
	w.broadcastResponse(finalMsg, traceID)                         // reason.response — text content
	w.broadcastReasonEnd(traceID, StopReason(finalMsg.StopReason)) // reason.end — lifecycle signal

	w.isReasoning = false
	w.mu.Unlock()

	// calls tryReason again to catch overlapping events.
	w.tryReason(ctx)
}

// metaCapabilityCall reports whether any tool call in msg targets a registered
// meta capability (one whose event is worker.update / worker.query, exposed to
// the LLM by its default tool name). When present, the round's tool calls are excluded
// from the transcript and the meta operation runs via its own event.
func (w *BaseReasonWorker) metaCapabilityCall(msg llm.Message) (llm.ContentBlock, bool) {
	for _, b := range msg.Content {
		if b.Type == llm.ContentToolCall {
			if cap, ok := w.capabilityByToolName(b.ToolName); ok && cap.Event != event.TypeToolRequest {
				return b, true
			}
		}
	}
	return llm.ContentBlock{}, false
}

// stripToolCalls returns a copy of msg with all ContentToolCall blocks removed,
// keeping thinking/text. Used when a meta tool call is present, since meta
// operations never produce a tool result (their call must not enter the
// transcript, or it would dangle without a paired tool_result).
func stripToolCalls(msg llm.Message) llm.Message {
	cleaned := make([]llm.ContentBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type != llm.ContentToolCall {
			cleaned = append(cleaned, b)
		}
	}
	msg.Content = cleaned
	return msg
}

// ensureToolCallIDs assigns a globally-unique id to every tool call in msg
// that lacks one. It mutates the message in place: the same blocks are later
// read by the transcript, the tracker and the tool-result patch, so a
// synthesized id propagates to both the assistant entry and its tool_result.
// Ids are monotonic across rounds because the tracker keeps pending calls
// between rounds — a per-turn "call_0" would collide with a still-pending one
// from an earlier round and mispair a result against the wrong call.
func (w *BaseReasonWorker) ensureToolCallIDs(msg llm.Message) {
	for i := range msg.Content {
		if msg.Content[i].Type == llm.ContentToolCall && msg.Content[i].ToolCallID == "" {
			msg.Content[i].ToolCallID = fmt.Sprintf("call_%d", atomic.AddUint64(&w.toolCallSeq, 1))
		}
	}
}

func (w *BaseReasonWorker) handleToolCalls(ctx context.Context, toolCalls []llm.ContentBlock, traceID string) {
	busCalls := toolCalls

	// Meta capabilities (event != tool.request) directly edit this worker's
	// own state and bypass the tool lifecycle: no placeholder, no tracker, no
	// dispatch. If any call in this batch targets a meta capability, the batch
	// is handled by the meta path: the meta call is converted back into its
	// worker.update / worker.query event and sent to self, which reason
	// processes asynchronously.
	var metaCall *llm.ContentBlock
	var metaCap Capability
	for i := range busCalls {
		if cap, ok := w.capabilityByToolName(busCalls[i].ToolName); ok && cap.Event != event.TypeToolRequest {
			metaCall = &busCalls[i]
			metaCap = cap
			break
		}
	}
	if metaCall != nil {
		// All tool calls were already excluded from the transcript in
		// finishReasoning once a meta capability was present, so nothing here
		// needs pairing. If a newer input already requested reasoning, the
		// meta operation yields (dropped — nothing is dispatched or applied);
		// the next round re-decides.
		if w.needReason {
			log.Printf("[reason %s] meta capability %s yielded to pending input", w.ID(), metaCall.ToolName)
			w.isReasoning = false
			w.mu.Unlock()
			w.tryReason(ctx)
			return
		}

		// Convert the tool call back into the capability's event (e.g.
		// context_compress → worker.update op=context.compress) and send it to
		// self, routed via the bus for audit; the meta operation completes
		// asynchronously and schedules the next round.
		argsMap := map[string]any{}
		if metaCall.ToolArguments != "" {
			json.Unmarshal([]byte(metaCall.ToolArguments), &argsMap)
		}
		if metaCap.KeyField != "" {
			argsMap[metaCap.KeyField] = metaCap.Key
		}
		evt := event.New(metaCap.Event, w.ID(), argsMap)
		evt.TraceID = w.currentTraceID
		_ = w.Channel.Send(ctx, evt, w.ID())

		w.isReasoning = false
		w.mu.Unlock()
		// No tryReason here: the next round is scheduled when the meta
		// operation completes (and buffered input is flushed).
		return
	}

	// Record the current round's single timeout timer, if any. At most one
	// is meaningful - the first timer to fire parks all pending tools; a
	// second set_tool_timeout in the same round is treated as a misplaced
	// config and overwrites the first (leak accepted). Only record if a
	// provider actually offers the timeout tool.
	for _, tc := range busCalls {
		if t, ok := w.timeoutToolFor(tc.ToolName); ok {
			w.activeTimeout = tc.ToolCallID
			w.activeTimeoutProvider = t.Provider
		}
	}

	w.transcript.Apply(ToolPlaceholdersPatch{Calls: busCalls})

	// Group tool calls by target worker, then publish tool.request
	// for each group. The bus routes each batch to the correct worker.
	// Unknown tools (not in w.tools, e.g. hallucinated by the LLM) are
	// failed immediately instead of broadcast — broadcasting confuses
	// other workers that subscribe to tool.request.
	toolNames := make([]string, len(busCalls))
	for i, tc := range busCalls {
		toolNames[i] = tc.ToolName
	}
	log.Printf("[reason %s] requesting %d tool call(s) via bus: %v", w.ID(), len(busCalls), toolNames)
	callsByTarget := make(map[string][]llm.ContentBlock)
	var unavailable []string
	for _, tc := range busCalls {
		var failReason string
		// Own tool capabilities (default tool name = tool name) loop back to
		// this worker; they are served by the registered tool handler.
		if cap, ok := w.capabilityByToolName(tc.ToolName); ok && cap.Event == event.TypeToolRequest {
			callsByTarget[w.ID()] = append(callsByTarget[w.ID()], tc)
			continue
		}
		t, ok := w.tools[tc.ToolName]
		if !ok {
			// Unknown / hallucinated: nothing on the bus declares it.
			failReason = "tool not available"
		} else if orig, ok := w.toolNameMap[tc.ToolName]; ok {
			// Hand the target worker back its original declared tool name via
			// the saved mapping (provider__program.search -> program.search).
			tc.ToolName = orig
			callsByTarget[t.Provider] = append(callsByTarget[t.Provider], tc)
			continue
		} else {
			// Known to w.tools but absent from the mapping: a name mismatch.
			// Do not guess by stripping a prefix; fail it like an unavailable
			// tool (no dispatch, error tool_result, and a tool_unavailable notice).
			failReason = "no reverse mapping"
		}

		// Shared failure tail for undispatchable calls (not found, or mapping
		// missing): record in the unavailable list, write an error tool_result
		// into the transcript, and do not dispatch.
		log.Printf("[reason %s] unavailable tool %s (%s) - not dispatched", w.ID(), tc.ToolName, failReason)
		unavailable = append(unavailable, tc.ToolName)
		w.transcript.Apply(ToolResultPatch{
			CallID: tc.ToolCallID,
			Name:   tc.ToolName,
			Text:   "Unknown tool '" + tc.ToolName + "': " + failReason + " - not dispatched.",
			IsErr:  true,
		})
	}
	for target, calls := range callsByTarget {
		w.toolCallTracker.Add(target, calls)
		w.sendToolRequests(target, w.ID(), calls, traceID)
	}
	if len(unavailable) > 0 {
		// Surface the fact that some tool calls could not be dispatched, so
		// the user sees it in the stream instead of a silent stall.
		log.Printf("[reason %s] unavailable tool call(s): %s", w.ID(), strings.Join(unavailable, ", "))
		evt := event.New("reason.response", w.ID(), map[string]any{
			"content":     []any{fmt.Sprintf("tool call unavailable: %s", strings.Join(unavailable, ", "))},
			"stop_reason": "tool_unavailable",
		})
		evt.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), evt)
	}
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}

func (w *BaseReasonWorker) broadcastResponse(msg llm.Message, traceID string) {
	var texts []any
	for _, block := range msg.Content {
		if block.Type == llm.ContentText {
			texts = append(texts, block.Text)
		}
	}
	evt := event.New("reason.response", w.ID(), map[string]any{
		"content":     texts,
		"stop_reason": msg.StopReason,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
	log.Printf("[reason %s] published reason.response, text_count=%d", w.ID(), len(texts))
}

func (w *BaseReasonWorker) broadcastReasonStart(traceID string) {
	evt := event.New("reason.start", w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

// StopReason values for broadcastReasonEnd.
// LLM provider stop reasons ("stop", "length", "tool_calls", etc.) are passed through as-is.
type StopReason string

const (
	StopReasonInterrupted StopReason = "interrupted"  // reasoning interrupted mid-stream
	StopReasonError       StopReason = "error"        // LLM call failed
	StopReasonAborted     StopReason = "aborted"      // abort received, no reasoning was in flight
	StopReasonRateLimited StopReason = "rate_limited" // 429: round paused for reminder-based backoff
)

func (w *BaseReasonWorker) broadcastReasonEnd(traceID string, stopReason StopReason) {
	evt := event.New("reason.end", w.ID(), map[string]any{
		"worker_id":   w.ID(),
		"stop_reason": string(stopReason),
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *BaseReasonWorker) broadcastThinking(blocks []llm.ContentBlock, traceID string) {
	log.Printf("[reason %s] publishing thinking: %d blocks, total %d chars", w.ID(), len(blocks), len(blocks[0].Text))
	var texts []any
	for _, b := range blocks {
		texts = append(texts, b.Text)
	}
	evt := event.New("reason.thinking", w.ID(), map[string]any{
		"content": texts,
	})
	evt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *BaseReasonWorker) broadcastThinkingDelta(text, traceID string) {
	evt := event.New("reason.thinking_delta", w.ID(), map[string]any{
		"delta": text,
	})
	evt.TraceID = traceID
	evt.Transient = true // live-streaming only, not durable history
	_ = w.Channel.Broadcast(context.Background(), evt)
}

func (w *BaseReasonWorker) broadcastTextDelta(text, traceID string) {
	evt := event.New("reason.text_delta", w.ID(), map[string]any{
		"delta": text,
	})
	evt.TraceID = traceID
	evt.Transient = true // live-streaming only, not durable history
	_ = w.Channel.Broadcast(context.Background(), evt)
}

// broadcastErrorAndEnd broadcasts an error response and ends the current reasoning
// round. Expects w.mu to be held; unlocks it and calls tryReason before returning.
func (w *BaseReasonWorker) broadcastErrorAndEnd(ctx context.Context, traceID string, err error) {
	log.Printf("[reason %s] LLM error: %v", w.ID(), err)
	errEvt := event.New("reason.response", w.ID(), map[string]any{
		"content":     []any{fmt.Sprintf("Error: %v", err)},
		"stop_reason": "error",
	})
	errEvt.TraceID = traceID
	_ = w.Channel.Broadcast(context.Background(), errEvt)
	w.broadcastReasonEnd(traceID, StopReasonError)
	w.isReasoning = false
	w.mu.Unlock()
	w.tryReason(ctx)
}
