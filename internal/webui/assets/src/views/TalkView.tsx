import { useMemo, useCallback, useState, type CSSProperties } from 'react'
import ViewHeader from '../components/ViewHeader'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { isToolResult } from '../components/talk-utils'
import type { EventPayload } from '../types'
import { useChatScroll } from './talk/useChatScroll'
import TalkRows from './talk/TalkRows'
import StreamingTail from './talk/StreamingTail'
import ScrollToBottomButton from './talk/ScrollToBottomButton'
import { computeStreamingTraces, computeToolPartials, type StreamTrace } from './talk/streaming'
import type { RowCtx } from './talk/RowCtx'

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

  const { scrollRef, atBottom, handleScroll, markManual, scrollToBottom, scrollToEvent } = useChatScroll({
    relevantEvents, events, onLoadMore, scrollToBottomSignal, onFollowChange,
  })

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
  // per-row in TalkRows so each row re-renders only when its own data changes.
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

  if (relevantEvents.length === 0 && streamingTraces.length === 0) {
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
          <TalkRows
            relevantEvents={relevantEvents}
            allEvents={events}
            responseOnly={responseOnly}
            talkWorkers={talkWorkers}
            resultByRequestId={resultByRequestId}
            decisionByRequestId={decisionByRequestId}
            toolPartials={toolPartials}
            ctx={ctx}
          />
          <StreamingTail traces={streamingTraces} responseOnly={responseOnly} ctx={ctx} />
        </div>
        {/* Floating jump-to-bottom: shown while the conversation isn't pinned
            to the bottom. Clicking re-pins so live updates follow again. */}
        {!atBottom && (
          <ScrollToBottomButton onClick={scrollToBottom} title={t('app.scrollToBottom')} colors={colors} />
        )}
      </div>
    </>
  )
}