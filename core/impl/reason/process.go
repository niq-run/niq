// Event dispatch and the transcript-input translation it needs.
//
// process routes a single event to one handler; each handler
// converts the event into a transcript input. This is the "what to
// do with an event" half of the event loop (the loop itself lives in worker.go).
//
// Input modes form a spectrum of increasing intrusion:
//
//	schedule (level 1):  append message, schedule only if idle, no park
//	append (level 2):    append message, schedule, park on next round
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

	"github.com/niq-run/niq/core/impl/reason/requesttracker"
	"github.com/niq-run/niq/core/impl/reason/transcript"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
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
		// Answer the discoverer DIRECTLY (not by broadcasting a ready to the
		// whole fleet). The storm was: every reason worker re-broadcast its
		// ready in reply to every discover, so N discovers × N workers × N
		// subscribers = O(N³) deliveries. A directed reply to the asker is the
		// complete answer at O(N) per discover — and the startup broadcast still
		// discloses this worker to whoever is up, so nothing is lost. Replying
		// to our own discover is pointless (we know ourselves).
		if evt.WorkerId != w.ID() {
			w.AnnounceReadyTo(evt.WorkerId, "reason", nil, false)
		}
	case evt.Type == event.TypeWorkerAbort:
		w.handleAbort(evt)
	case evt.Type == TypeTimerTimeout:
		w.handleTimeout(evt)
	case evt.Type == event.TypeWorkerReady:
		w.HandleWorkerReady(evt)
	case evt.Type == event.TypeWorkerGone:
		w.handleWorkerGone(evt)
	case isToolResultEvent(evt.Type):
		w.handleToolResult(evt)
	case evt.Type == event.TypeWorkerInput:
		w.handleInput(evt)
	default:
		// Extension channel for any other event type: own tool events
		// (send_message, context.compress, provider.*) route here to the
		// registered extension by event type. An unregistered event is dropped.
		if !w.DispatchExtension(evt) {
			log.Printf("[reason %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
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

// handleTimeout processes a timer.timeout event (from timeout).
// Records a system message and schedules a fresh reasoning round.
// Stale timers (cancelled or prior batch) are silently discarded.
func (w *BaseReasonWorker) handleTimeout(evt event.Event) {
	timerID, _ := evt.Payload[timerIDField].(string)

	// Only the current round's timer is meaningful; an orphaned timer from a
	// prior round (whose call_id no longer matches) is silently discarded.
	if w.activeTimeout != "" && timerID == w.activeTimeout {
		w.captureTraceID(evt)
		msg := llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "[system] tool call timeout"}},
		}
		w.appendInput([]llm.Message{msg}, requesttracker.PreemptCauseTimeout)
	}
}

// handleToolResult processes a request.completed/failed/rejected event.
// Normal path: a Pending call resolves — replace its [pending] placeholder
// with the outcome. Late path: a Parked call matched — append a contextualized
// user message (a second tool_result for the same call_id would violate the
// pairing invariant). Untracked call_ids (e.g. synthetic cancel responses) are
// ignored.
func (w *BaseReasonWorker) handleToolResult(evt event.Event) {
	// Normal resolution of a Pending call. The result event no longer carries
	// the tool name (it is paired by request_id), so the name comes from the
	// matched tracked request.
	if tr := w.requestTracker.HandleResponse(evt); tr != nil {
		callID := evt.RequestId
		if callID != "" {
			content, isErr := resultOutcome(evt)
			w.transcript.Apply(transcript.ToolResultPatch{CallID: callID, Name: tr.Name, Content: content, IsErr: isErr})
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
		callID := evt.RequestId
		if callID != "" && parked.Name != "" {
			if content, _ := resultOutcome(evt); len(content) > 0 {
				w.transcript.Apply(transcript.LateResultPatch{
					CallID: callID, Name: parked.Name, Content: content, Cause: string(parked.ParkCause),
				})
			}
		}
		// A parked call resolved late — this is new information the model should
		// incorporate, so wake a follow-up reason round. The case that surfaces
		// this: an append/interrupt session parked a tool, the prompt round
		// answered *without* its result (it was told the call was interrupted),
		// and then the real result landed. Without waking, that stale
		// "interrupted" answer is never corrected. We route the wake through the
		// same idle gate as schedule-mode input (wakeIfIdle): never park an
		// in-flight tool to surface this. If another call is still being awaited,
		// waking would park it (prepareReasoning parks all pending tools),
		// interrupting that tool too — and its own late result would then wake
		// another round, cascading. The late result is already appended to the
		// transcript, and the in-flight call's own resolution sets needReason once
		// every request settles, so that later round still sees it. Causes that
		// mean a deliberate stop (abort, restart) stay quiet entirely.
		switch parked.ParkCause {
		case requesttracker.PreemptCauseAbort, requesttracker.PreemptCauseRestart:
		default:
			w.wakeIfIdle()
		}
	}
}

// handleInput processes an input event (worker.input — from a human UI, a peer
// reason worker, or a reminder timer). The input_mode field picks one of the
// three levels (see package comment):
//
//	"schedule"  — level 1: append the message and schedule a round, but only
//	             when the system is idle (no in-flight reasoning, no pending
//	             tool calls). Does not interrupt or park anything.
//	"append"   — level 2 (the DEFAULT stance, also used when input_mode is
//	             absent or unknown): append the message as a supplement to the
//	             ongoing thought and schedule a fresh round, parking pending
//	             tools when it starts, without interrupting an in-flight call
//	             (a gentle wake-up, like a reminder).
//	"interrupt" — level 3: cancel the in-flight reasoning call and schedule a
//	             fresh round; pending tools are parked when reason() starts
//	             (using the stored cause). Interrupting a mid-thought worker is
//	             opt-in — a machine message should carry this explicitly.
func (w *BaseReasonWorker) handleInput(evt event.Event) {
	w.captureTraceID(evt)

	msgs := w.convertEvent(evt)

	mode, _ := evt.Payload["input_mode"].(string)
	switch mode {
	case "schedule":
		w.scheduleInput(msgs)
	case "interrupt":
		w.interruptInput(msgs, requesttracker.PreemptCauseInput)
	default:
		// append is the default: absent or unrecognized input_mode — including
		// hiw's legacy "default" — is a gentle append, not an interrupt.
		w.appendInput(msgs, requesttracker.PreemptCauseInput)
	}
}

// recallToolCalls best-effort cancels in-flight tool calls by sending a
// request.cancel event to each call's target worker.
func (w *BaseReasonWorker) recallToolCalls(tcs []*requesttracker.TrackedRequest) {
	byTarget := make(map[string][]string)
	for _, rc := range tcs {
		if rc.TargetID != "" {
			byTarget[rc.TargetID] = append(byTarget[rc.TargetID], rc.CallID)
		}
	}

	for target, callIDs := range byTarget {
		for _, callID := range callIDs {
			evt := event.New(event.TypeRequestCancel, w.ID(), nil)
			evt.RequestId = callID
			evt.TraceID = w.currentTraceID
			_ = w.Channel.Send(context.Background(), evt, target)
		}
	}
}

// wakeIfIdle schedules a fresh reasoning round only when the system is idle
// - no in-flight reasoning and no tool call still being awaited. When busy,
// the caller keeps its message in the transcript and lets the wake happen when
// the current work finishes (an in-flight call's own resolution sets needReason
// once Resolved). Both schedule-mode input (level 1) and a resolving late
// result route through this gate, so a late result never parks an in-flight
// tool - which would interrupt that tool too and cascade.
func (w *BaseReasonWorker) wakeIfIdle() {
	if !w.isReasoning && w.requestTracker.Resolved() {
		w.needReason = true
	}
}

// scheduleInput appends messages and schedules a new round only when the
// system is idle - no in-flight reasoning and no pending tool calls. Does not
// interrupt or park anything; when busy, the message waits in the transcript
// until the current work finishes. This is the least intrusive input mode
// (level 1).
func (w *BaseReasonWorker) scheduleInput(msgs []llm.Message) {
	w.transcript.Apply(transcript.InputPatch{Messages: msgs})
	w.wakeIfIdle()
}

// appendInput appends messages, records the cause, and schedules a fresh
// reasoning round. The message supplements the ongoing thought: an in-flight
// reasoning call is not interrupted, but pending tools are parked when the
// next round starts so it responds promptly. This is the moderate input mode
// (level 2).
func (w *BaseReasonWorker) appendInput(msgs []llm.Message, cause requesttracker.PreemptCause) {
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

// parkRequests parks all pending tool calls with the given cause and rewrites
// each one's transcript placeholder with the explanation. Returns the parked
// calls for callers that need to recall them (e.g., handleAbort).
func (w *BaseReasonWorker) parkRequests(cause requesttracker.PreemptCause) []*requesttracker.TrackedRequest {
	tcs := w.requestTracker.ParkAll(cause)
	for _, rc := range tcs {
		w.transcript.Apply(transcript.ToolParkedPatch{
			CallID: rc.CallID,
			Name:   rc.Name,
			Cause:  string(cause),
		})
	}
	return tcs
}

// parkPending is parkRequests plus the current round's timeout: once nothing is
// awaited, the round's timer is no longer needed. Restore does not use it —
// there is no round (and no timer) to cancel on a worker that has not started.
func (w *BaseReasonWorker) parkPending(cause requesttracker.PreemptCause) []*requesttracker.TrackedRequest {
	tcs := w.parkRequests(cause)
	w.cancelTimeout()
	return tcs
}

// cancelTimeout cancels the current round's active timeout timer by sending a
// request.cancel event to the worker that set it. Called when all tools have
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
	evt := event.New(event.TypeRequestCancel, w.ID(), map[string]any{
		timerIDField: timerID,
	})
	evt.RequestId = timerID + "-cancel"
	_ = w.Channel.Send(context.Background(), evt, w.activeTimeoutProvider)

	w.activeTimeout = ""
	w.activeTimeoutProvider = ""
}

// ── event → transcript input translation ──

// isToolResultEvent reports whether the given event type is a tool
// lifecycle event consumed by the LLM worker.
func isToolResultEvent(typ event.EventType) bool {
	return typ == event.TypeRequestCompleted || typ == event.TypeRequestFailed || typ == event.TypeRequestRejected
}

// convertEvent routes an event through the registered EventConverters.
// Matching uses the subscription's full semantics (type + optional source).
func (w *BaseReasonWorker) convertEvent(evt event.Event) []llm.Message {
	for _, h := range w.eventConverters {
		if h.Pattern.Matches(evt) {
			return h.Converter(evt)
		}
	}
	// worker.input understands the structured content payload (image base64 /
	// uploaded file paths) before falling back to the plain-text default.
	if evt.Type == event.TypeWorkerInput {
		return ConvertInputEvent(evt)
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

// ConvertInputEvent turns a worker.input event into user messages. A payload
// payload carrying a structured "content" array is decoded into content blocks via the
// shared transcript.PayloadToLlmContent decoder; otherwise the event falls back
// to the plain-text DefaultConverter shape.
func ConvertInputEvent(evt event.Event) []llm.Message {
	if blocks, ok := transcript.PayloadToLlmContent(evt.Payload); ok {
		blocks = append(blocks, eventFooter(evt))
		return []llm.Message{{Role: llm.RoleUser, Content: blocks}}
	}
	return DefaultConverter(evt)
}

// eventFooter is the metadata footer appended to a converted input message.
func eventFooter(evt event.Event) llm.ContentBlock {
	return llm.ContentBlock{Type: llm.ContentText,
		Text: fmt.Sprintf("[Event: %s from %s]", evt.Type, evt.WorkerId)}
}

// resultOutcome extracts the outcome content blocks and error flag from a
// tool result event. Used by both the normal-resolution and late-result paths.
//
// A payload carrying a structured "content" array is decoded into content
// blocks via the shared transcript.PayloadToLlmContent decoder, so tools can
// return multi-modal results (e.g. an image as a ContentImage block), not just
// text.
//
// When the payload has no content array, the bus does not constrain a reply's
// payload shape — external workers answer in their own vocabulary — so the
// recognized summary fields ("result" / "error" / "reason") are a preference,
// not a requirement: when none is present, the FULL payload is serialized as
// the tool's result text instead of being dropped. This mirrors
// DefaultConverter's philosophy for input events: a recognized field reads
// cleanly, and anything else still reaches the LLM losslessly.
func resultOutcome(evt event.Event) ([]llm.ContentBlock, bool) {
	isErr := false
	switch evt.Type {
	case event.TypeRequestFailed:
		isErr = true
	case event.TypeRequestRejected:
		isErr = true
	}

	// Structured multi-modal result takes precedence.
	if blocks, ok := transcript.PayloadToLlmContent(evt.Payload); ok {
		return blocks, isErr
	}

	var text string
	switch evt.Type {
	case event.TypeRequestCompleted:
		// A structured result (an object, an array, a number) is JSON text to the
		// model — the same thing a JSON-string reply used to carry, minus the
		// double encoding.
		text = event.ReplyText(evt.Payload)
	case event.TypeRequestFailed:
		if e, ok := evt.Payload["error"]; ok {
			text = "Tool call failed: " + fmt.Sprintf("%v", e)
		}
	case event.TypeRequestRejected:
		if r, ok := evt.Payload["reason"]; ok {
			text = "Tool call rejected: " + fmt.Sprintf("%v", r)
		}
	default:
		return nil, false
	}
	if text == "" && len(evt.Payload) > 0 {
		if b, err := json.Marshal(evt.Payload); err == nil {
			text = string(b)
		}
	}
	if text == "" {
		return nil, isErr
	}
	return []llm.ContentBlock{{Type: llm.ContentText, Text: text}}, isErr
}
