// Streaming-content accumulation for the talk view: reason.*_delta traces and
// request.progressed tool partials. Pure functions over the event list, so
// TalkView can memoize them on their inputs.
import type { EventPayload } from '../../types'

// StreamTrace is one in-flight reason trace being accumulated from
// reason.*_delta events. Thinking and text are tracked (and finalized)
// independently: a trace is only dropped from the live view once BOTH its
// terminal reason.thinking AND reason.response have arrived. Finalizing the
// whole trace on the first terminal (reason.thinking fires before
// reason.response) would drop the response-text deltas that stream between the
// two, so the answer would appear only at the very end instead of live.
export type StreamTrace = { traceId: string; thinking: string; text: string; workerId: string; lastTs: number; thinkingDone: boolean; textDone: boolean }

// matchesSelectedWorker reports whether an event's envelope involves one of the
// currently selected talk workers (sender, target, or a recipient). With no
// selection every event passes; the caller separately handles the "all reason
// workers" scope. Shared by relevantEvents and the streaming accumulators so
// the scope rule stays in exactly one place.
export function matchesSelectedWorker(evt: EventPayload, talkWorkers: Set<string>, deliveries: Record<string, string[]>): boolean {
  if (talkWorkers.size === 0) return true
  if (talkWorkers.has(evt.worker_id) || talkWorkers.has(evt.target_worker_id)) return true
  const recipients = deliveries[evt.id] || evt.recipients
  return !!recipients && recipients.some(r => talkWorkers.has(r))
}

// computeStreamingTraces accumulates reason.*_delta by trace_id. Each phase
// (thinking / text) is dropped once its own terminal event arrives, and the
// trace is removed from the list only when nothing live is left — so thinking
// streams until reason.thinking, then the response text keeps streaming until
// reason.response.
export function computeStreamingTraces(events: EventPayload[], talkWorkers: Set<string>, deliveries: Record<string, string[]>): StreamTrace[] {
  const map: Record<string, { thinking: string; text: string; workerId: string; lastTs: number; thinkingDone: boolean; textDone: boolean }> = {}
  for (const evt of events) {
    const t = evt.type
    if (t !== 'reason.thinking_delta' && t !== 'reason.text_delta' &&
        t !== 'reason.thinking' && t !== 'reason.response') continue
    const tid = evt.trace_id
    if (!tid) continue
    // Respect the same talkWorkers scope as relevantEvents.
    if (!matchesSelectedWorker(evt, talkWorkers, deliveries)) continue
    const entry = map[tid]
    if (!entry) {
      map[tid] = { thinking: '', text: '', workerId: evt.worker_id, lastTs: evt.timestamp, thinkingDone: false, textDone: false }
    } else {
      entry.lastTs = evt.timestamp
    }
    const cur = map[tid]
    // Terminal events flip only their own phase; don't return early here so the
    // peer phase (e.g. text streams arriving after reason.thinking) keeps
    // building the live block.
    if (t === 'reason.thinking') { cur.thinkingDone = true; continue }
    if (t === 'reason.response') { cur.textDone = true; continue }
    // A phase never resumes after its terminal: later stray deltas (possible
    // across eventbus reordering) are ignored.
    if (t === 'reason.thinking_delta' && !cur.thinkingDone) cur.thinking += (evt.payload?.delta as string) || ''
    else if (t === 'reason.text_delta' && !cur.textDone) cur.text += (evt.payload?.delta as string) || ''
  }
  // Keep a trace only while a live (not-yet-finalized and non-empty) portion
  // still needs streaming; drop it once there is nothing left to show.
  return Object.entries(map)
    .filter(([, v]) => (!v.thinkingDone && v.thinking !== '') || (!v.textDone && v.text !== ''))
    .map(([traceId, v]) => ({ traceId, thinking: v.thinking, text: v.text, workerId: v.workerId, lastTs: v.lastTs, thinkingDone: v.thinkingDone, textDone: v.textDone }))
}

// computeToolPartials accumulates request.progressed output by request_id, and
// clears a call's partial once it reaches a terminal event.
export function computeToolPartials(events: EventPayload[], talkWorkers: Set<string>, deliveries: Record<string, string[]>): Record<string, string> {
  const map: Record<string, string> = {}
  for (const evt of events) {
    if (evt.type !== 'request.progressed') continue
    const callId = (evt.request_id as string) || ''
    if (!callId) continue
    // Respect the same talkWorkers scope as relevantEvents.
    if (!matchesSelectedWorker(evt, talkWorkers, deliveries)) continue
    const partial = (evt.payload?.partial as string) || ''
    map[callId] = (map[callId] || '') + partial
  }
  // Once a tool reaches a terminal event (completed/failed/rejected/cancel),
  // its result card takes over and the live streaming content in the
  // invocation card is cleared. Progressed events are NOT terminal — they
  // keep accumulating above.
  for (const evt of events) {
    if (evt.type === 'request.completed' || evt.type === 'request.failed' || evt.type === 'request.rejected' || evt.type === 'request.cancel') {
      const callId = (evt.request_id as string) || ''
      if (callId) delete map[callId]
    }
  }
  return map
}
