import { useMemo, useRef, useEffect, useLayoutEffect, useCallback, useState, type ReactNode, type CSSProperties } from 'react'
import ViewHeader from '../components/ViewHeader'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import ThinkingBlock from '../components/ThinkingBlock'
import ResponseBlock from '../components/ResponseBlock'
import { isReasonBoundary, isToolEvent, isToolInvocation, isToolResult, toolCallId } from '../components/talk-utils'
import type { EventPayload } from '../types'
import { useChatScroll } from './talk/useChatScroll'
import {
  WorkerBadge, RowBadge, InputRow, AbortRow, TimerReminderRow, TimeoutRow,
  CancelRow, InterruptedRow, ThinkingRow, ResponseRow, ApprovalRow, ToolRow,
  type RowCtx,
} from './talk/rows'

interface TalkViewProps {
  events: EventPayload[]
  talkWorkers: Set<string>
  onTraceClick: (traceId: string) => void
  onLoadMore?: () => void
  onMention?: (workerId: string) => void
  // Right-clicking a worker badge opens its detail page.
  onOpenDetail?: (workerId: string) => void
  // Talk-avatar context-menu actions: add a worker to the watched set, or focus
  // it as the only watched worker.
  onAddFilter?: (workerId: string) => void
  onFocusWorker?: (workerId: string) => void
  deliveries: Record<string, string[]>
  humanId?: string
  workerTypes?: Record<string, string>
  thinkingExpanded: boolean
  compactMode: boolean
  streamingMode: boolean
  responseOnly: boolean
  // Mobile changes bubble widths and the tool title layout; desktop keeps the
  // original look.
  isMobile: boolean
  // Inline quick-approve for approval.request cards: forwards the human's
  // verdict to the HIW (App wires it to the decisions API).
  onDecide?: (id: string, approved: boolean, note?: string) => void
  // Bumped by App each time the user sends a message. Sending is a strong
  // "return to the live bottom" signal: even if the user had scrolled up to
  // read, we re-engage the follow and scroll to the newest content.
  scrollToBottomSignal?: number
  // Reports the sticky follow-to-bottom switch state (true = pinned/following)
  // whenever it changes, so App can e.g. only trim the event cache while the
  // live tail is being followed.
  onFollowChange?: (following: boolean) => void
}

// StreamTrace is one in-flight reason trace being accumulated from
// reason.*_delta events. Thinking and text are tracked (and finalized)
// independently: a trace is only dropped from the live view once BOTH its
// terminal reason.thinking AND reason.response have arrived. Finalizing the
// whole trace on the first terminal (reason.thinking fires before
// reason.response) would drop the response-text deltas that stream between the
// two, so the answer would appear only at the very end instead of live.
type StreamTrace = { traceId: string; thinking: string; text: string; workerId: string; lastTs: number; thinkingDone: boolean; textDone: boolean }

// matchesSelectedWorker reports whether an event's envelope involves one of the
// currently selected talk workers (sender, target, or a recipient). With no
// selection every event passes; the caller separately handles the "all reason
// workers" scope. Shared by relevantEvents and the streaming accumulators so
// the scope rule stays in exactly one place.
function matchesSelectedWorker(evt: EventPayload, talkWorkers: Set<string>, deliveries: Record<string, string[]>): boolean {
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
function computeStreamingTraces(events: EventPayload[], talkWorkers: Set<string>, deliveries: Record<string, string[]>): StreamTrace[] {
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
function computeToolPartials(events: EventPayload[], talkWorkers: Set<string>, deliveries: Record<string, string[]>): Record<string, string> {
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

// GrowingHeight previously animated the streaming tail's height over a CSS
// transition. That multi-frame transition interleaved with the pinned-bottom
// trim (a single commit collapsing the top), which is what made the sealed
// bottom visibly "jump up then get pulled down". With no animation, growth and
// trim are both single-commit — the sync pin in useChatScroll then holds the
// bottom fixed through both in the same frame. It now just renders its child
// at natural height (streaming batches appear instantly / "pop").
function GrowingHeight({ children }: { children: ReactNode }) {
  return <div>{children}</div>
}

export default function TalkView({ events, talkWorkers, onTraceClick, onLoadMore, onMention, onOpenDetail, onAddFilter, onFocusWorker, deliveries, humanId = 'webui-hiw', workerTypes = {}, thinkingExpanded, compactMode, streamingMode, responseOnly, isMobile, onDecide, scrollToBottomSignal, onFollowChange }: TalkViewProps) {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  // Left-side bubbles are wider on phones (90%) and keep the original 70% on
  // desktop.
  const bubbleMax = isMobile ? '90%' : '70%'
  // Only reason workers (the conversation partners) get a standalone avatar
  // row; other workers' events carry their worker ID inline in the block title.
  const isReason = useCallback((wid: string) => workerTypes[wid] === 'reason', [workerTypes])
  // Convert the human worker's id into a friendlier "you" for display. The
  // [webui] channel marker stays untranslated (it is a transport name): the
  // same human also speaks through other transports (lark bridge, ...), each
  // with its own worker id, so marking the channel tells the reader which
  // door the message came through.
  const displayName = useCallback(
    (wid?: string) => (wid && wid === humanId ? `${t('talk.you')}[webui]` : wid ?? ''),
    [humanId, t],
  )
  // Direction of the worker identity in a block title. Left-aligned blocks
  // read from the reason worker's side ("to X" = it sends, "from: X" =
  // someone sent it in). Right-aligned blocks sit visually as outgoing
  // toward the reason worker, so they always read "to <reason worker>" —
  // regardless of who the sender was.
  const directionOf = useCallback((evt: EventPayload, alignRight?: boolean): string => {
    if (alignRight && evt.target_worker_id) {
      return `to ${displayName(evt.target_worker_id)}`
    }
    if (isReason(evt.worker_id)) {
      return evt.target_worker_id ? `to ${displayName(evt.target_worker_id)}` : ''
    }
    return evt.worker_id ? `from: ${displayName(evt.worker_id)}` : ''
  }, [displayName, isReason])
  // Unified rule for which message bubbles sit on the right. The right side is
  // reserved for messages addressed to a specific reason worker:
  //   (b) the human (hiw) -> that reason worker
  //   (a) a system worker (non-reason, non-hiw) -> that reason worker
  //   (c) another reason worker -> that reason worker, but only when that
  //       worker is the SINGLE currently selected talk worker
  // Self-directed envelopes are the worker's own actions and stay left: the
  // request convention sends a worker's own tool calls (send_message,
  // context.compress, ...) to the worker itself, so worker_id == target.
  // Everything else — reason<->reason outside the single-select case, or a
  // message with no (non-reason) target — goes to the left.
  const isRightAligned = (evt: EventPayload): boolean => {
    // The current user's own messages always sit on the right, whether they
    // were directed at a reason worker or broadcast (e.g. a lone "@").
    if (evt.worker_id === humanId) return true
    const target = evt.target_worker_id
    if (!target || !isReason(target)) return false
    if (target === evt.worker_id) return false // self-directed: own tool call
    if (!isReason(evt.worker_id)) return true // (a) system -> reason
    return talkWorkers.size === 1 && talkWorkers.has(target) // (c)
  }
  const [expandedContent, setExpandedContent] = useState<Set<string>>(new Set())
  // Tool/request cards are an accordion: at most one open, so only one code
  // body is rendered/highlighted at a time (a perf win on code-heavy chats).
  const [openToolId, setOpenToolId] = useState<string | null>(null)

  const toggleExpanded = useCallback((key: string) => {
    setExpandedContent(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }, [])
  const toggleTool = useCallback((key: string) => {
    setOpenToolId(prev => (prev === key ? null : key))
  }, [])

  // Talk is the selected reason workers' conversation: what they sent, and
  // what was sent to them — from HIW or any worker alike. When nothing is
  // selected the scope is every reason worker. Events whose envelope involves
  // no reason worker at all (the human UI driving a workspace directly, and
  // that worker's replies to the UI) are simply outside this conversation —
  // scope does the filtering, no blacklist needed.
  const inReasonScope = (id?: string): boolean => {
    if (!id) return false
    if (talkWorkers.size > 0) return talkWorkers.has(id)
    return workerTypes[id] === 'reason'
  }
  // Defers scope filtering until the worker types are known, so the timeline
  // doesn't start empty on first paint (the workers poll fills it instantly).
  const workerTypesLoaded = Object.keys(workerTypes).length > 0

  // Filter events by selected workers.
  const relevantEvents = useMemo(() => {
    return events.filter(evt => {
      if (evt.type.startsWith('worker.') && evt.type !== 'worker.input' && evt.type !== 'worker.abort') return false
      // Delta / partial events are never rendered as standalone rows. When
      // streaming mode is on they're consumed to build the streaming UI;
      // when off they're dropped entirely — the terminal events (reason.*,
      // request.*) carry the aggregated content.
      if (evt.type === 'reason.thinking_delta' || evt.type === 'reason.text_delta' || evt.type === 'request.progressed') return false
      // Approval chains ride on the originating caller, not the envelope
      // (workspace → approver): include when that caller is in scope. UI-
      // initiated chains (caller = the human UI) are outside the reason
      // conversation; the Approvals view owns them.
      if (evt.type === 'approval.request') {
        // origin is the current field name; worker_id is the pre-rename
        // spelling still present in persisted history.
        const caller = evt.payload?.origin ?? evt.payload?.worker_id
        return typeof caller === 'string' && inReasonScope(caller)
      }
      if (talkWorkers.size === 0 && !workerTypesLoaded) return true
      const recipients = deliveries[evt.id] || evt.recipients
      return inReasonScope(evt.worker_id) ||
        inReasonScope(evt.target_worker_id) ||
        (recipients || []).some(inReasonScope)
    })
  }, [events, talkWorkers, deliveries, workerTypes])

  // One pass over the events builds both request-pairing lookups: request_id →
  // the terminal result event (so a card can show its answer), and
  // request_id → the approval decision event (for inline approve/reject state).
  // Kept as a single scan rather than two so the per-render work stays small.
  const requestMaps = useMemo(() => {
    const results: Record<string, EventPayload> = {}
    const decisions: Record<string, EventPayload> = {}
    for (const evt of events) {
      if (evt.request_id) {
        if (isToolResult(evt.type)) results[evt.request_id] = evt
        else if (evt.type === 'approval.decision') decisions[evt.request_id] = evt
      }
    }
    return { results, decisions }
  }, [events])
  const { results: resultByRequestId, decisions: decisionByRequestId } = requestMaps

  // Streaming content (reason.*_delta and request.progressed) recomputed
  // directly whenever its inputs change; empty when streamingMode is off. The
  // backend throttles delta bursts, so no client-side coalescing interval.
  const streamingTraces = useMemo<StreamTrace[]>(() => (
    streamingMode ? computeStreamingTraces(events, talkWorkers, deliveries) : []
  ), [events, talkWorkers, deliveries, streamingMode])
  const toolPartials = useMemo<Record<string, string>>(() => (
    streamingMode ? computeToolPartials(events, talkWorkers, deliveries) : {}
  ), [events, talkWorkers, deliveries, streamingMode])
  // [streamdbg] Log the streaming-UI state whenever the signature changes, so we
  // can see whether the live tail is actually building as deltas arrive. Remove
  // once diagnosed.
  const streamSig = streamingMode + '|' + responseOnly + '|' + streamingTraces.length + '|' +
    streamingTraces.map(x => (x.traceId || '').slice(0, 6) + ':t' + x.thinking.length + ':x' + x.text.length + ':' + (x.thinkingDone ? 'T' : 's') + (x.textDone ? 'T' : 's')).join(',')
  useEffect(() => { console.log('[streamdbg] UI', streamSig) }, [streamSig])
const { scrollRef, atBottom, handleScroll, markManual, scrollToBottom, scrollToEvent } = useChatScroll({
    relevantEvents, events, onLoadMore, scrollToBottomSignal, onFollowChange,
  })
  // Track the last DISPLAYED avatar's worker id. Avatars render for reason
  // workers and the human; hidden workers (workspace, timer, ...) don't reset
  // the streak, so a single speaker's avatar stays until the speaker switches.
// ── Build rows ─────────────────────────────────────────────────────────
  // Shared box metrics for tool-style blocks (also read by the row components).
  const tPad = compactMode ? '4px 8px' : '6px 12px'
  const tFontSize = compactMode ? fontSizes.xs : fontSizes.base
  // Separator between title segments: a left border on each segment (instead
  // of a standalone "|"), so a wrapped line never ends with a dangling bar.
  const itemSep: CSSProperties = { borderLeft: '1px solid ' + colors.textDimmed, paddingLeft: 8 }

  // Shared context handed to every row component. Memoized so its reference is
  // stable across live events (the derived helper closures are useCallback'd),
  // which is what lets the memoized rows skip re-rendering when only the
  // volatile per-row data changed. The per-event volatile lookups (result/
  // decision/partial/allEvents) are deliberately NOT in ctx — they're resolved
  // per-row in the dispatch below so each row re-renders only when its own data
  // changes.
  const ctx = useMemo<RowCtx>(() => ({
    dark, colors, t, isMobile, compactMode, thinkingExpanded, bubbleMax,
    tPad, tFontSize, itemSep, humanId, displayName, isReason, directionOf,
    expandedContent, toggleExpanded, openToolId, toggleTool, onMention, onOpenDetail, onAddFilter, onFocusWorker, onTraceClick, onDecide,
    scrollToEvent,
  }), [
    dark, colors, t, isMobile, compactMode, thinkingExpanded, bubbleMax,
    tPad, tFontSize, itemSep, humanId, displayName, isReason, directionOf,
    expandedContent, toggleExpanded, openToolId, toggleTool, onMention, onOpenDetail, onAddFilter, onFocusWorker, onTraceClick, onDecide,
    scrollToEvent,
  ])

  const nodes: React.ReactNode[] = []

  // The avatar streak is the only cross-row state: a row shows a speaker avatar
  // only when the speaker changes (and never for the no-avatar notice types).
  // We resolve it in a cheap prepass, then dispatch each surviving event to its
  // row component — no per-type JSX lives in the main body anymore.
  let lastAvatarId = ''
  const rowFacts: { evt: EventPayload; alignRight: boolean; showBadge: boolean }[] = []
  for (const evt of relevantEvents) {
    if (isReasonBoundary(evt.type)) continue
    // Response-only mode hides the intermediate process — thinking, reasoning
    // interruptions, tool invocations (the domain-typed request starters) and
    // the request.* lifecycle (cancels). The reason.response / worker.input
    // terminal content is the only thing left visible.
    if (responseOnly && (evt.type === 'reason.thinking' || evt.type === 'reason.interrupted' || isToolEvent(evt.type) || isToolInvocation(evt.type))) continue
    // A terminal result answers an invocation and is merged into that
    // invocation's card; it never renders as its own row. Skipping it before
    // the avatar bookkeeping also avoids its callee worker id advancing the
    // streak invisibly.
    if (isToolResult(evt.type) && evt.request_id) continue
    const alignRight = isRightAligned(evt)
    const shouldShowAvatar = isReason(evt.worker_id) || evt.worker_id === humanId || alignRight
    const showBadge = shouldShowAvatar && evt.worker_id !== lastAvatarId &&
      evt.type !== 'reason.interrupted' && evt.type !== 'request.cancel'
    if (showBadge) lastAvatarId = evt.worker_id
    rowFacts.push({ evt, alignRight, showBadge })
  }

  // Trailing avatar: at the end of every speaker run (the last row sharing one
  // display avatar) stamp a small avatar — on the block's right edge — so the
  // reader always knows whose output this is without scrolling back to the top
  // badge. Always shown (no height heuristic), matching the compact footer look.
  const trails: Record<number, string> = {}
  {
    let runAvatar = ''
    for (let i = 0; i < rowFacts.length; i++) {
      const f = rowFacts[i]
      if (f.showBadge) runAvatar = f.evt.worker_id
      const runEnds = (i === rowFacts.length - 1) || rowFacts[i + 1].showBadge
      if (runEnds && runAvatar) trails[i] = runAvatar
    }
  }
  for (let i = 0; i < rowFacts.length; i++) {
    const { evt, alignRight, showBadge } = rowFacts[i]
    switch (evt.type) {
      case 'worker.input':
        nodes.push(<InputRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />)
        break
      case 'worker.abort':
        nodes.push(<AbortRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />)
        break
      case 'timer.reminder':
        nodes.push(<TimerReminderRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />)
        break
      case 'timer.timeout':
        nodes.push(<TimeoutRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />)
        break
      case 'request.cancel':
        nodes.push(<CancelRow key={evt.id} evt={evt} ctx={ctx} />)
        break
      case 'reason.interrupted':
        nodes.push(<InterruptedRow key={evt.id} evt={evt} ctx={ctx} />)
        break
      case 'reason.thinking':
        nodes.push(<ThinkingRow key={evt.id} evt={evt} showBadge={showBadge} ctx={ctx} />)
        break
      case 'reason.response':
        nodes.push(<ResponseRow key={evt.id} evt={evt} showBadge={showBadge} ctx={ctx} allEvents={events} />)
        break
      case 'approval.request': {
        const decision = decisionByRequestId[toolCallId(evt)]
        nodes.push(<ApprovalRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} decision={decision} ctx={ctx} />)
        break
      }
      default: {
        const callId = toolCallId(evt)
        const resultEvt = resultByRequestId[callId]
        const partial = toolPartials[callId] || ''
        nodes.push(<ToolRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} resultEvt={resultEvt} partial={partial} ctx={ctx} />)
        break
      }
    }
    // Stamp a small trailing avatar on the LEFT block's right edge at the end of
    // every speaker run. Right-aligned messages already carry their avatar on
    // the right, so they don't need the extra marker. It reuses WorkerBadge, so
    // the trailing avatar has the same left-click (mention) and right-click
    // (detail / focus) affordances as the top badge.
    const trail = trails[i]
    if (trail && !alignRight) {
      nodes.push(
        <div key={evt.id + '-trail'} style={{ display: 'flex', justifyContent: 'flex-end', maxWidth: bubbleMax, margin: '2px 0 16px' }}>
          <WorkerBadge
            id={trail}
            show={true}
            small
            humanId={humanId}
            isReason={isReason}
            onMention={onMention}
            onOpenDetail={onOpenDetail}
            onFocusWorker={onFocusWorker}
            displayName={displayName}
          />
        </div>,
      )
    }
  }
  // Header: always visible, not in scroll area — shown even when empty. On
  // mobile the app-level top bar (hamburger + view label) already heads the
  // page, so this in-view header is skipped to avoid a double header.
  const header = !isMobile ? (
    <ViewHeader
      title={t('nav.talk')}
      right={
        <span style={{ fontSize: fontSizes.sm, color: colors.textMuted }}>
          {t('talk.watching')} <strong style={{ color: colors.textDim }}>{
            talkWorkers.size > 0
              ? `[${[...talkWorkers].join(', ')}]`
              : t('events.allWorkers')
          }</strong>
        </span>
      }
    />
  ) : null

  if (nodes.length === 0 && streamingTraces.length === 0) {
    const label = talkWorkers.size > 0
      ? [...talkWorkers].join(', ')
      : t('talk.allWorkers')
    return (
      <>
        {header}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: colors.textDimmed }}>
          <p style={{ fontSize: fontSizes.md }}>{t('talk.empty', { label })}</p>
        </div>
      </>
    )
  }

  return (
    <>
      {header}
      {/* Relative wrapper hosts the floating scroll-to-bottom button just
          above the input box (the input is the next sibling in App's column). */}
      <div style={{ flex: 1, position: 'relative', display: 'flex', minHeight: 0, minWidth: 0 }}>
        <div
          ref={scrollRef}
          onScroll={handleScroll}
          onWheel={markManual}
          onPointerDown={markManual}
          onTouchStart={markManual}
          onClick={(e) => {
            // Clicking blank space collapses any expanded tool/approval blocks.
            // Tool cards are a single-open accordion; approvals are their own set.
            if (expandedContent.size === 0 && openToolId === null) return
            const el = e.target as Element
            const scroller = scrollRef.current
            if (!scroller) return
            if (el === scroller || el.parentElement === scroller) {
              setExpandedContent(new Set())
              setOpenToolId(null)
            }
          }}
          style={{ flex: 1, minWidth: 0, overflowY: 'auto', overflowX: 'hidden', padding: '0 24px 60px', overflowAnchor: 'none' }}
        >
        {nodes}
        {!responseOnly && streamingTraces.map(({ traceId, thinking, text, workerId, lastTs, thinkingDone, textDone }) => {
          // Only the not-yet-finalized phase streams: once reason.thinking
          // lands, the terminal ThinkingBlock (rendered among nodes) takes
          // over thinking and the live block keeps streaming just the
          // response text until reason.response.
          const showThinking = !!thinking && !thinkingDone
          const showText = !!text && !textDone
          if (!showThinking && !showText) return null
          const synthetic = (type: 'reason.thinking' | 'reason.response', content: string): EventPayload => ({
            id: `stream-${type}-${traceId}`,
            type,
            worker_id: workerId,
            target_worker_id: '',
            timestamp: lastTs,
            trace_id: traceId,
            payload: { content: [content] },
          })
          return (
            <div key={`stream-${traceId}`} style={{ maxWidth: bubbleMax, marginBottom: 12 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
                <WorkerBadge id={workerId} show={true} humanId={humanId} isReason={isReason} onMention={onMention} onOpenDetail={onOpenDetail} displayName={displayName} />
                <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, fontStyle: 'italic' }}>● streaming</span>
              </div>
              {/* No height animation on the streaming tail: growth is a single
                  commit, so the sync pin in useChatScroll holds the bottom fixed
                  through both growth and a pinned trim (no frame interleave). */}
              {showThinking && (
                <GrowingHeight>
                  <ThinkingBlock evt={synthetic('reason.thinking', thinking)} defaultExpanded={thinkingExpanded} compact={compactMode} />
                </GrowingHeight>
              )}
              {showText && (
                <GrowingHeight>
                  <ResponseBlock evt={synthetic('reason.response', text)} />
                </GrowingHeight>
              )}
            </div>
          )
        })}
        </div>
        {/* Floating jump-to-bottom: shown while the conversation isn't pinned
            to the bottom. Clicking re-pins so live updates follow again. */}
        {!atBottom && (
          <button
            onClick={scrollToBottom}
            title={t('app.scrollToBottom')}
            style={{
              position: 'absolute', bottom: 12, left: '50%', transform: 'translateX(-50%)',
              width: 36, height: 36, borderRadius: '50%', padding: 0,
              border: '1px solid ' + colors.border, background: colors.bg, color: colors.textDim,
              cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
              boxShadow: '0 2px 8px rgba(0,0,0,0.25)', zIndex: 5,
            }}
          >
            {/* Drawn arrow — glyph baselines sit off-center in the mono font. */}
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
              <path d="M7 2v9M3.2 7.6 7 11.4 10.8 7.6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
            </svg>
          </button>
        )}
      </div>
    </>
  )
}
