// Event dispatch and the transcript-input translation it needs.
//
// process routes a single event to one handler; each handler
// converts the event into a transcript input. This is the "what to
// do with an event" half of the event loop (the loop itself lives in worker.go).
//
// Input modes form a spectrum of increasing intrusion:
//
//	append (level 1):    append message, schedule if idle, no park
//	remind (level 2):    append message, schedule, park on next round
//	interrupt (level 3): interrupt in-flight reasoning, schedule, park
//
// None of the three is a default; each caller picks one per input by setting
// input_mode. hiw, for instance, chooses interrupt by default, but that is its
// own choice, not a property of the level.
package reason

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/baseworker"
	"github.com/niq-run/niq/pkg/reason/requesttracker"
	"github.com/niq-run/niq/pkg/reason/transcript"
)

// - worker.ready/gone: learn/forget a worker's tools & events
// - tool result: resolve / park-late / update placeholder
// - input: convert to messages + schedule (level 1/2/3 via input_mode)
func (w *BaseReasonWorker) process(_ context.Context, evt event.Event) {
	w.mu.Lock()
	defer w.mu.Unlock()

	log.Printf("[reason %s] event: %s (from=%s)", w.ID(), evt.Type, evt.WorkerId)

	switch {
	case evt.Type == event.TypeWorkerDiscover:
		if evt.WorkerId != w.ID() {
			w.broadcastReady()
		}
	case evt.Type == event.TypeWorkerAbort:
		w.handleAbort(evt)
	case evt.Type == event.TypeWorkerUpdate:
		// Extension channel: route to the registered handler by op.
		if !w.DispatchExtension(evt) {
			log.Printf("[reason %s] no extension for worker.update op=%v", w.ID(), evt.Payload["op"])
		}
	case evt.Type == event.TypeWorkerQuery:
		// Extension channel: route to the registered handler by subject.
		if !w.DispatchExtension(evt) {
			log.Printf("[reason %s] no extension for worker.query subject=%v", w.ID(), evt.Payload["subject"])
		}
	case evt.Type == "timer.timeout":
		w.handleTimeout(evt)
	case evt.Type == "timer.reminder":
		w.handleReminder(evt)
	case evt.Type == event.TypeWorkerReady:
		w.handleWorkerReady(evt)
	case evt.Type == event.TypeWorkerGone:
		w.handleWorkerGone(evt)
	case isToolResultEvent(evt.Type):
		w.handleToolResult(evt)
	case evt.Type == event.TypeToolRequest:
		// Extension channel: a tool.request targeting this worker. The
		// registered tool handler serves it; an unknown tool is rejected.
		tc := baseworker.ParseToolCall(evt)
		if !w.DispatchExtension(evt) {
			w.replyUnknownTool(tc)
		}
	case evt.Type == event.TypeWorkerInput:
		w.handleInput(evt)
	}
}

// ContextCompressOpEvent is the convention every reason worker honors: under
// window pressure the mechanism emits a worker.update with this op, and the
// worker responds by shrinking its own transcript. The mechanism fires the
// event; it never performs or books the edit itself — that is the worker's
// strategy, reached by handling this event. Rotate and any other context op
// are the worker's own extras; the mechanism does not emit them.
//
// The concrete worker registers its compress extension under this same key
// (rather than hardcoding the literal), so the convention has a single source
// of truth and renaming it cannot desynchronize the two sides.
const ContextCompressOpEvent = "context.compress"

// emitContextCompress sends a worker.update event to this worker, requesting
// the context.compress convention operation through the bus for audit — the
// single path every compress trigger converges on. The mechanism only ever
// auto-emits this one convention event; the concrete worker implements the
// shrink strategy and does its own bookkeeping. Lock need not be held
// specially; the event is queued to self and handled asynchronously by process.
func (w *BaseReasonWorker) emitContextCompress(ctx context.Context) {
	args := map[string]any{"op": ContextCompressOpEvent}
	evt := event.New(event.TypeWorkerUpdate, w.ID(), args)
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(ctx, evt, w.ID())
}

// handleAbort cancels the current LLM call, parks all pending tools (so late
// results can still be contextualized), best-effort recalls them, and records
// the abort in the  needReason is cleared so no new
// reasoning round starts until the next worker.input.
func (w *BaseReasonWorker) handleAbort(_ event.Event) {
	w.interruptReason = requesttracker.PreemptCauseAbort

	// Broadcast reason end
	hadReasoning := w.cancelReason != nil
	if !hadReasoning {
		// No reasoning round was in flight to publish the interrupt lifecycle.
		w.broadcastReasonEnd(w.currentTraceID, StopReasonAborted)
	}

	// Cancel current running reason
	if w.cancelReason != nil {
		w.cancelReason()
		w.cancelReason = nil
	}

	// Park and recall tool calls
	tcs := w.parkPending(requesttracker.PreemptCauseAbort)
	w.recallToolCalls(tcs)

	// Record the abort in the working transcript so the LLM
	// knows what happened when the next round starts.
	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: fmt.Sprintf("[system] reasoning was aborted. %d tool call(s) parked.", len(tcs))}},
	}}})

	w.needReason = false
}

// handleTimeout processes a timer.timeout event (from set_tool_timeout).
// Records a system message and schedules a fresh reasoning round.
// Stale timers (cancelled or prior batch) are silently discarded.
func (w *BaseReasonWorker) handleTimeout(evt event.Event) {
	timerID, _ := evt.Payload["timer_id"].(string)

	// Only the current round's timer is meaningful; an orphaned timer from a
	// prior round (whose call_id no longer matches) is silently discarded.
	if w.activeTimeout != "" && timerID == w.activeTimeout {
		w.captureTraceID(evt)
		msg := llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "[system] tool call timeout"}},
		}
		w.scheduleInput([]llm.Message{msg}, requesttracker.PreemptCauseTimeout)
	}
}

// handleReminder processes a timer.reminder event (from elapse). It wakes the
// LLM — the event payload carries the purpose so the LLM knows what to do. A
// reminder is a gentle wake-up: it schedules fresh reasoning without cancelling
// any in-flight reasoning call.
func (w *BaseReasonWorker) handleReminder(evt event.Event) {
	w.captureTraceID(evt)
	msgs := w.convertEvent(evt)
	w.scheduleInput(msgs, requesttracker.PreemptCauseReminder)
}

// handleToolResult processes a tool.completed/failed/rejected event.
// Normal path: a Pending call resolves — replace its [pending] placeholder
// with the outcome. Late path: a Parked call matched — append a contextualized
// user message (a second tool_result for the same call_id would violate the
// pairing invariant). Untracked call_ids (e.g. synthetic cancel responses) are
// ignored.
func (w *BaseReasonWorker) handleToolResult(evt event.Event) {
	// Normal resolution of a Pending call.
	if w.requestTracker.HandleResponse(evt) {
		callID, _ := evt.Payload["call_id"].(string)
		if callID != "" {
			name, _ := evt.Payload["name"].(string)
			text, isErr := resultOutcome(evt)
			w.transcript.Apply(transcript.ToolResultPatch{CallID: callID, Name: name, Text: text, IsErr: isErr})
		}
		// All tool calls resolved
		if w.requestTracker.Resolved() {
			w.cancelTimeout()
			w.needReason = true
		}
		return
	}

	// Late result for a Parked call.
	if parked := w.requestTracker.ResolveLate(evt); parked != nil {
		callID, _ := evt.Payload["call_id"].(string)
		name, _ := evt.Payload["name"].(string)
		if callID != "" && name != "" {
			if text, _ := resultOutcome(evt); text != "" {
				w.transcript.Apply(transcript.LateResultPatch{
					CallID: callID, Name: name, Text: text, Cause: string(parked.ParkCause),
				})
			}
		}
	}
}

// handleInput processes an input event. The input_mode field in the payload
// picks one of the three levels (see package comment):
//
//	"append" — level 1: append the message and schedule a new round, but only
//	            when the system is idle (no in-flight reasoning, no pending
//	            tool calls). Does not interrupt or park anything.
//	"schedule" — level 2: append the message and schedule a fresh round, parking
//	            pending tools when it starts, without interrupting an in-flight
//	            call (a gentle wake-up, like a reminder).
//	else (incl. hiw's usual "default") — level 3 interrupt: cancel the
//	            in-flight reasoning call and schedule a fresh round; pending
//	            tools are parked when reason() starts (using the stored cause).
func (w *BaseReasonWorker) handleInput(evt event.Event) {
	w.captureTraceID(evt)

	msgs := w.convertEvent(evt)

	mode, _ := evt.Payload["input_mode"].(string)
	switch mode {
	case "append":
		w.appendInput(msgs)
	case "schedule":
		w.scheduleInput(msgs, requesttracker.PreemptCauseInput)
	default:
		w.interruptInput(msgs, requesttracker.PreemptCauseInput)
	}
}

// recallToolCalls best-effort cancels in-flight tool calls by sending a
// tool.cancel event to each call's target worker.
func (w *BaseReasonWorker) recallToolCalls(tcs []*requesttracker.TrackedRequest) {
	byTarget := make(map[string][]string)
	for _, rc := range tcs {
		if rc.TargetID != "" {
			byTarget[rc.TargetID] = append(byTarget[rc.TargetID], rc.CallID)
		}
	}

	for target, callIDs := range byTarget {
		for _, callID := range callIDs {
			evt := event.New(event.TypeToolCancel, w.ID(), map[string]any{
				"call_id": callID,
			})
			evt.TraceID = w.currentTraceID
			_ = w.Channel.Send(context.Background(), evt, target)
		}
	}
}

// appendInput appends messages and schedules a new round only when the system
// is idle - no in-flight reasoning and no pending tool calls. Does not
// interrupt or park anything. This is the least intrusive input mode (level 1).
func (w *BaseReasonWorker) appendInput(msgs []llm.Message) {
	w.transcript.Apply(transcript.InputPatch{Messages: msgs})

	if !w.isReasoning && w.requestTracker.Resolved() {
		w.needReason = true
	}
}

// scheduleInput appends messages, records the cause, and schedules a fresh
// reasoning round. The pending tools are parked when reason() starts, not
// here. This is the moderate input mode (level 2) — it does not interrupt
// an in-flight reasoning call, but ensures the next round responds promptly.
func (w *BaseReasonWorker) scheduleInput(msgs []llm.Message, cause requesttracker.PreemptCause) {
	w.transcript.Apply(transcript.InputPatch{Messages: msgs})
	w.immediateReasoningCause = cause
	w.needReason = true
}

// interruptInput cancels the in-flight reasoning call, records the cause,
// and schedules a fresh round. The pending tools are parked when reason()
// starts. This is the strongest input mode (level 3) — it interrupts the
// current LLM call so the new input is handled immediately.
func (w *BaseReasonWorker) interruptInput(msgs []llm.Message, cause requesttracker.PreemptCause) {
	w.transcript.Apply(transcript.InputPatch{Messages: msgs})
	w.interruptReason = cause
	if w.cancelReason != nil {
		w.cancelReason()
		w.cancelReason = nil
	}
	w.immediateReasoningCause = cause
	w.needReason = true
}

// captureTraceID propagates the trace_id from an incoming event into the
// worker's current trace, so all events published during the reasoning round
// it triggers share the same trace. Events without a trace_id leave it unchanged.
func (w *BaseReasonWorker) captureTraceID(evt event.Event) {
	if evt.TraceID != "" {
		w.currentTraceID = evt.TraceID
	}
}

// parkPending parks all pending tool calls with the given cause and cancels the
// current round's timeout timer. Returns the parked calls for callers that need
// to recall them (e.g., handleAbort).
func (w *BaseReasonWorker) parkPending(cause requesttracker.PreemptCause) []*requesttracker.TrackedRequest {
	tcs := w.requestTracker.ParkAll(cause)
	for _, rc := range tcs {
		w.transcript.Apply(transcript.ToolParkedPatch{
			CallID: rc.CallID,
			Name:   rc.Name,
			Cause:  string(cause),
		})
	}
	w.cancelTimeout()
	return tcs
}

// cancelTimeout cancels the current round's active timeout timer by sending a
// tool.cancel event to the worker that set it. Called when all tools have
// resolved — the timeout is no longer needed.
func (w *BaseReasonWorker) cancelTimeout() {
	if w.activeTimeout == "" {
		return
	}
	// Cancel goes to the worker that set the active timeout (captured at set
	// time) — not re-looked-up, so it is correct even with multiple providers.
	if w.activeTimeoutProvider == "" {
		return
	}
	timerID := w.activeTimeout
	evt := event.New(event.TypeToolCancel, w.ID(), map[string]any{
		"call_id":  timerID + "-cancel",
		"timer_id": timerID,
	})
	_ = w.Channel.Send(context.Background(), evt, w.activeTimeoutProvider)

	w.activeTimeout = ""
	w.activeTimeoutProvider = ""
}

// ── event → transcript input translation ──

// isToolResultEvent reports whether the given event type is a tool
// lifecycle event consumed by the LLM worker.
func isToolResultEvent(typ event.EventType) bool {
	return typ == event.TypeToolCompleted || typ == event.TypeToolFailed || typ == event.TypeToolRejected
}

// convertEvent routes an event through the registered EventConverters.
// Matching uses the subscription's full semantics (type + optional source).
func (w *BaseReasonWorker) convertEvent(evt event.Event) []llm.Message {
	for _, h := range w.eventConverters {
		if h.Pattern.Matches(evt) {
			return h.Converter(evt)
		}
	}
	return DefaultConverter(evt)
}

// DefaultConverter formats an event as a plain-text user message.
// Convention: if the payload contains a "text" field, it is used as the
// primary content (first line), followed by the event metadata and full
// payload JSON. This lets event producers provide a human-readable summary
// while preserving the full structured data for the LLM.
func DefaultConverter(evt event.Event) []llm.Message {
	payloadStr := "{}"
	if evt.Payload != nil {
		if b, err := json.Marshal(evt.Payload); err == nil {
			payloadStr = string(b)
		}
	}

	text, _ := evt.Payload["text"].(string)
	if text != "" {
		return []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{
				Type: llm.ContentText,
				Text: fmt.Sprintf("%s\n\n[Event: %s from %s]", text, evt.Type, evt.WorkerId),
			}},
		}}
	}

	return []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{
			Type: llm.ContentText,
			Text: fmt.Sprintf("[Event: %s from %s]\n%s", evt.Type, evt.WorkerId, payloadStr),
		}},
	}}
}

// resultOutcome extracts the human-readable outcome text and error flag from a
// tool result event. Used by both the normal-resolution and late-result paths.
func resultOutcome(evt event.Event) (string, bool) {
	switch evt.Type {
	case event.TypeToolCompleted:
		if r, ok := evt.Payload["result"]; ok {
			return fmt.Sprintf("%v", r), false
		}
	case event.TypeToolFailed:
		if e, ok := evt.Payload["error"]; ok {
			return "Tool call failed: " + fmt.Sprintf("%v", e), true
		}
	case event.TypeToolRejected:
		if r, ok := evt.Payload["reason"]; ok {
			return "Tool call rejected: " + fmt.Sprintf("%v", r), true
		}
	}
	return "", false
}

// resultOutcome extracts the human-readable outcome text and error flag from a
// tool result event. Used by both the normal-resolution and late-result paths.
