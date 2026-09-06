import { useMemo, useRef, useEffect, useLayoutEffect, useCallback, useState, type ReactNode, type CSSProperties } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { makeMdComponents } from '../components/MarkdownComponents'
import CollapsibleCode from '../components/CollapsibleCode'
import ThinkingBlock from '../components/ThinkingBlock'
import ViewHeader from '../components/ViewHeader'
import ResponseBlock from '../components/ResponseBlock'
import SystemReminderBlock from '../components/SystemReminderBlock'
import {
  getInputText, isToolEvent, isToolResult, isReasonBoundary,
  toolContent, toolSummary, toolCallId,
  formatTime, findReferencedInput, splitSystemReminder, parseAttachments,
} from '../components/talk-utils'
import type { EventPayload } from '../types'

interface TalkViewProps {
  events: EventPayload[]
  talkWorkers: Set<string>
  onTraceClick: (traceId: string) => void
  onLoadMore?: () => void
  onMention?: (workerId: string) => void
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
}

// StreamTrace is one in-flight reason trace being accumulated from
// reason.*_delta events, dropped once its final reason.thinking / reason.response
// arrives.
type StreamTrace = { traceId: string; thinking: string; text: string; workerId: string; lastTs: number }

// computeStreamingTraces accumulates reason.*_delta by trace_id. A trace is
// dropped once its terminal reason.thinking / reason.response event arrives.
function computeStreamingTraces(events: EventPayload[], talkWorkers: Set<string>, deliveries: Record<string, string[]>): StreamTrace[] {
  const map: Record<string, { thinking: string; text: string; workerId: string; lastTs: number }> = {}
  const finalized = new Set<string>()
  for (const evt of events) {
    const t = evt.type
    if (t !== 'reason.thinking_delta' && t !== 'reason.text_delta' &&
        t !== 'reason.thinking' && t !== 'reason.response') continue
    const tid = evt.trace_id
    if (!tid) continue
    if (t === 'reason.thinking' || t === 'reason.response') {
      finalized.add(tid)
      continue
    }
    if (finalized.has(tid)) continue
    // Respect the same talkWorkers filter as relevantEvents.
    if (talkWorkers.size > 0) {
      const recipients = deliveries[evt.id] || evt.recipients
      if (!talkWorkers.has(evt.worker_id) &&
          !talkWorkers.has(evt.target_worker_id) &&
          !(recipients && recipients.some(r => talkWorkers.has(r)))) continue
    }
    if (!map[tid]) map[tid] = { thinking: '', text: '', workerId: evt.worker_id, lastTs: evt.timestamp }
    const delta = (evt.payload?.delta as string) || ''
    if (t === 'reason.thinking_delta') map[tid].thinking += delta
    else map[tid].text += delta
    map[tid].lastTs = evt.timestamp
  }
  return Object.entries(map)
    .filter(([tid]) => !finalized.has(tid))
    .map(([tid, v]) => ({ traceId: tid, ...v }))
}

// computeToolPartials accumulates request.progressed output by request_id, and
// clears a call's partial once it reaches a terminal event.
function computeToolPartials(events: EventPayload[], talkWorkers: Set<string>, deliveries: Record<string, string[]>): Record<string, string> {
  const map: Record<string, string> = {}
  for (const evt of events) {
    if (evt.type !== 'request.progressed') continue
    const callId = (evt.request_id as string) || ''
    if (!callId) continue
    // Respect the same talkWorkers filter as relevantEvents.
    if (talkWorkers.size > 0) {
      const recipients = deliveries[evt.id] || evt.recipients
      if (!talkWorkers.has(evt.worker_id) &&
          !talkWorkers.has(evt.target_worker_id) &&
          !(recipients && recipients.some(r => talkWorkers.has(r)))) continue
    }
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

// ── Worker name label (avatar) ──
// Only reason workers are mentionable: they get the hover @ and a click-to-@
// action. Other speakers' avatars are plain labels (no @). The human worker id
// renders as "you". Defined at module scope so its identity is stable across
// renders — a component defined inside TalkView would be re-created on every
// render, unmount/remount all badges, and drop the hover state (the @ would
// flash and disappear even while the pointer stays put).
function WorkerBadge({ id, show, humanId, isReason, onMention, displayName }: {
  id: string
  show: boolean
  humanId: string
  isReason: (id: string) => boolean
  onMention?: (id: string) => void
  displayName: (id?: string) => string
}) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [hover, setHover] = useState(false)
  if (!show) return null
  const isHuman = id === humanId
  const mentionable = !isHuman && isReason(id) && !!onMention
  return (
    <span
      onClick={(e) => { e.stopPropagation(); if (mentionable) onMention?.(id) }}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      className={mentionable ? 'badge-mention' : undefined}
      style={{
        cursor: mentionable ? 'pointer' : 'default',
        fontSize: fontSizes.xxl,
        color: colors.accent,
        fontWeight: 'bold',
        fontFamily: 'monospace',
      }}
    >
      {mentionable && (
        <span style={{ display: 'inline-block', overflow: 'hidden', whiteSpace: 'nowrap', verticalAlign: 'bottom', maxWidth: hover ? '1ch' : 0, transition: 'max-width 0.18s' }}>@</span>
      )}
      {displayName(id)}
      {mentionable && <span className="badge-tip">{t('badge.mention.tip')}</span>}
    </span>
  )
}

export default function TalkView({ events, talkWorkers, onTraceClick, onLoadMore, onMention, deliveries, humanId = 'webui-hiw', workerTypes = {}, thinkingExpanded, compactMode, streamingMode, responseOnly, isMobile, onDecide }: TalkViewProps) {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  // Left-side bubbles are wider on phones (90%) and keep the original 70% on
  // desktop.
  const bubbleMax = isMobile ? '90%' : '70%'
  // Only reason workers (the conversation partners) get a standalone avatar
  // row; other workers' events carry their worker ID inline in the block title.
  const isReason = (wid: string) => workerTypes[wid] === 'reason'
  // Convert the human worker's id into a friendlier "you" for display. The
  // [webui] channel marker stays untranslated (it is a transport name): the
  // same human also speaks through other transports (lark bridge, ...), each
  // with its own worker id, so marking the channel tells the reader which
  // door the message came through.
  const displayName = (wid?: string) => (wid && wid === humanId ? `${t('talk.you')}[webui]` : wid ?? '')
  // Direction of the worker identity in a block title. Left-aligned blocks
  // read from the reason worker's side ("to X" = it sends, "from: X" =
  // someone sent it in). Right-aligned blocks sit visually as outgoing
  // toward the reason worker, so they always read "to <reason worker>" —
  // regardless of who the sender was.
  const directionOf = (evt: EventPayload, alignRight?: boolean): string => {
    if (alignRight && evt.target_worker_id) {
      return `to ${displayName(evt.target_worker_id)}`
    }
    if (isReason(evt.worker_id)) {
      return evt.target_worker_id ? `to ${displayName(evt.target_worker_id)}` : ''
    }
    return evt.worker_id ? `from: ${displayName(evt.worker_id)}` : ''
  }
  // Unified rule for which message bubbles sit on the right. The right side is
  // reserved for messages addressed to a specific reason worker:
  //   (b) the human (hiw) -> that reason worker
  //   (a) a system worker (non-reason, non-hiw) -> that reason worker
  //   (c) another reason worker -> that reason worker, but only when that
  //       worker is the SINGLE currently selected talk worker
  // Everything else — reason<->reason outside the single-select case, or a
  // message with no (non-reason) target — goes to the left.
  const isRightAligned = (evt: EventPayload): boolean => {
    // The current user's own messages always sit on the right, whether they
    // were directed at a reason worker or broadcast (e.g. a lone "@").
    if (evt.worker_id === humanId) return true
    const target = evt.target_worker_id
    if (!target || !isReason(target)) return false
    if (!isReason(evt.worker_id)) return true // (a) system -> reason
    return talkWorkers.size === 1 && talkWorkers.has(target) // (c)
  }
  const scrollRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)
  // Mirrors autoScrollRef for rendering: the scroll-to-bottom button shows
  // while the conversation isn't pinned to the bottom.
  const [atBottom, setAtBottom] = useState(true)
  const [expandedContent, setExpandedContent] = useState<Set<string>>(new Set())
  const prevEventCount = useRef(0)
  const prevStreamLen = useRef(0)
  const hasInitialScrolled = useRef(false)
  // The first ~second after mounting is the initial population (e.g. after
  // switching to the Talk view): scrolls are instant so the page does not
  // animate from top to bottom. Real-time pushes after that smooth-scroll.
  const mountedAt = useRef(Date.now())

  const toggleExpanded = (key: string) => {
    setExpandedContent(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

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

  // request_id → terminal result event: the only pairing in the request
  // protocol. A card picks up its answer from here (the result row itself is
  // then skipped), so whether an event was answered is a lookup, not a guess.
  const resultByRequestId = useMemo(() => {
    const m: Record<string, EventPayload> = {}
    for (const evt of events) {
      if (isToolResult(evt.type) && evt.request_id) {
        m[evt.request_id] = evt
      }
    }
    return m
  }, [events])

  // approval.request's RequestId → its decision event, for the inline
  // quick-approve state on the approval card.
  const decisionByRequestId = useMemo(() => {
    const m: Record<string, EventPayload> = {}
    for (const evt of events) {
      if (evt.type === 'approval.decision' && evt.request_id) {
        m[evt.request_id] = evt
      }
    }
    return m
  }, [events])

  // Streaming content (reason.*_delta and request.progressed) is recomputed
  // whenever events change (see the effect below) — no client-side coalescing
  // tick; the backend throttles delta bursts.
  const eventsRef = useRef(events)
  eventsRef.current = events
  const [streamingTraces, setStreamingTraces] = useState<StreamTrace[]>([])
  const [toolPartials, setToolPartials] = useState<Record<string, string>>({})

  // Recompute streaming content whenever events change. The backend throttles
  // delta bursts, so there is no client-side coalescing interval: the live
  // streaming block tracks the latest deltas in real time and disappears the
  // instant its terminal reason.* / request.* event lands (computeStreamingTraces
  // already drops finalized traces), so it never lingers beside the full block.
  useEffect(() => {
    if (!streamingMode) {
      setStreamingTraces([])
      setToolPartials({})
      return
    }
    const evs = eventsRef.current
    setStreamingTraces(computeStreamingTraces(evs, talkWorkers, deliveries))
    setToolPartials(computeToolPartials(evs, talkWorkers, deliveries))
  }, [events, streamingMode, talkWorkers, deliveries])


  useEffect(() => {
    const count = relevantEvents.length
    const streamLen = streamingTraces.reduce((s, t) => s + t.thinking.length + t.text.length, 0) +
      Object.values(toolPartials).reduce((s, p) => s + p.length, 0)
    if (!hasInitialScrolled.current) {
      hasInitialScrolled.current = true
      prevEventCount.current = count
      prevStreamLen.current = streamLen
      // Initial render: land at the bottom instantly, no animation.
      if (scrollRef.current) {
        scrollRef.current.scrollTop = scrollRef.current.scrollHeight
      }
      return
    }
    const grew = count > prevEventCount.current || streamLen > prevStreamLen.current
    if (grew && autoScrollRef.current && scrollRef.current) {
      const animated = Date.now() - mountedAt.current > 1000
      setTimeout(() => {
        const el = scrollRef.current
        if (autoScrollRef.current && el) {
          // When already pinned to the bottom, follow new content instantly —
          // a smooth scroll from the bottom reads as a stutter/re-scroll.
          const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
          el.scrollTo({ top: el.scrollHeight, behavior: animated && !nearBottom ? 'smooth' : 'auto' })
        }
      }, 0)
    }
    prevEventCount.current = count
    prevStreamLen.current = streamLen
  }, [relevantEvents, streamingTraces, toolPartials])

  	const handleScroll = useCallback(() => {
  		const el = scrollRef.current
  		if (!el) return
  		const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
  		autoScrollRef.current = atBottom
  		// Drives the scroll-to-bottom button; React bails out when unchanged.
  		setAtBottom(atBottom)
  	}, [])

  	const scrollToBottom = useCallback(() => {
  		autoScrollRef.current = true
  		setAtBottom(true)
  		const el = scrollRef.current
  		if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  	}, [])

	// Scroll to an event node if it is present in the current list; no-op when
	// it is not (e.g. filtered out or loaded-on-request).
	const scrollToEvent = useCallback((evtId: string) => {
		const el = scrollRef.current
		if (!el) return
		const target = el.querySelector(`[data-evt-id="${CSS.escape(evtId)}"]`) as HTMLElement | null
		if (!target) return
		// Position relative to the scroll container (getBoundingClientRect is
		// viewport-based and stable regardless of offsetParent), then scroll so
		// the node's top sits just under the container top.
		autoScrollRef.current = false
		const top = target.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop - 12
		el.scrollTo({ top, behavior: 'smooth' })
	}, [])

  const nodes: React.ReactNode[] = []

  // Load more button at the top of the scrollable area
  const sentinelRef = useRef<HTMLDivElement>(null)
  const topLockRef = useRef(false)          // a loadMore prepend is in flight
  const prevScrollHeightRef = useRef(0)
  useEffect(() => {
    if (!onLoadMore || events.length === 0) return
    const el = sentinelRef.current
    if (!el) return
    const observer = new IntersectionObserver((entries) => {
      if (entries[0].isIntersecting) {
        topLockRef.current = true // keep viewport stable across the prepend
        onLoadMore()
      }
    }, { rootMargin: '200px 0px' })
    observer.observe(el)
    return () => observer.disconnect()
  }, [onLoadMore, events.length])

  // After older events are prepended at the top, nudge scrollTop down by the
  // amount the content grew so the visible frame doesn't jump.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el) return
    if (topLockRef.current) {
      topLockRef.current = false
      const grew = el.scrollHeight - prevScrollHeightRef.current
      if (grew > 0) el.scrollTop = Math.max(0, el.scrollTop + grew)
    }
    prevScrollHeightRef.current = el.scrollHeight
  }, [relevantEvents])

  // Track the last DISPLAYED avatar's worker id. Avatars render for reason
  // workers and the human; hidden workers (workspace, timer, ...) don't reset
  // the streak, so a single speaker's avatar stays until the speaker switches.
  let lastAvatarId = ''

  // Shared box metrics for tool-style blocks (tool calls, results, cancels).
  const tPad = compactMode ? '4px 8px' : '6px 12px'
  const tFontSize = compactMode ? fontSizes.xs : fontSizes.base
  // Separator between title segments: a left border on each segment (instead
  // of a standalone "|"), so a wrapped line never ends with a dangling bar.
  const itemSep: CSSProperties = { borderLeft: '1px solid ' + colors.textDimmed, paddingLeft: 8 }

  // Sentinel for auto-scroll-to-top loading
  const sentinel = onLoadMore && events.length > 0 ? (
    <div key="sentinel-top" ref={sentinelRef} style={{ height: 1 }} />
  ) : null
  if (sentinel) nodes.push(sentinel)

  for (const [i, evt] of relevantEvents.entries()) {
    if (isReasonBoundary(evt.type)) continue
    // Response-only mode: hide the intermediate process (thinking + tool calls,
    // including cancels).
    if (responseOnly && (evt.type === 'reason.thinking' || evt.type === 'reason.interrupted' || isToolEvent(evt.type))) continue

    // A terminal result answers an invocation: it is merged into that
    // invocation's card and never rendered as its own row. Skip it before the
    // avatar bookkeeping below: its worker_id is the callee (lark, host, ...)
    // and letting it advance the streak would break it invisibly, making the
    // real speaker's next event re-show the avatar.
    if (isToolResult(evt.type) && evt.request_id) continue

    // Placement: one rule for every block (isRightAligned). A dedicated
    // renderer may style an event its own way but never picks its own side.
    const alignRight = isRightAligned(evt)
    // Show a worker-name avatar for reason workers, the human, and any
    // right-aligned message (e.g. an external worker like the lark bridge
    // speaking to a reason worker) so its identity is visible.
    const shouldShowAvatar = isReason(evt.worker_id) || evt.worker_id === humanId || alignRight
    // Notice rows that render without an avatar (interrupted / cancelled) must
    // not consume the streak either: nothing identifying the speaker is
    // displayed, so a streak they set would be invisible.
    const showBadge = shouldShowAvatar && evt.worker_id !== lastAvatarId &&
      evt.type !== 'reason.interrupted' && evt.type !== 'request.cancel'
    if (showBadge) lastAvatarId = evt.worker_id

    // worker.input
    if (evt.type === 'worker.input') {
      // Attachment blocks never enter the markdown: they render as chips
      // (image thumbnails / file references) below the message text.
      const parsed = parseAttachments(getInputText(evt))
      const { reminder, content } = splitSystemReminder(parsed.text)
      // The sending UI selects one of three input levels (interrupt / schedule /
      // append). The event stores it as payload.input_mode; the web UI's own
      // default "interrupt" is emitted without the field, so an absent value on
      // a human message means interrupt. Surface the mode as a small grey italic
      // label on the bubble so the reader can tell the three apart.
      const rawMode = (evt.payload?.input_mode as string) || (evt.worker_id === humanId ? 'default' : '')
      const modeKey =
        rawMode === 'default' || rawMode === 'interrupt' ? 'interrupt'
        : rawMode === 'schedule' ? 'schedule'
        : rawMode === 'append' ? 'append' : null
      nodes.push(
        		<div key={evt.id} data-evt-id={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
        		  {showBadge && (
        			<div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
         		  <div
            			style={{
            			  maxWidth: alignRight ? '70%' : bubbleMax,
            			  minWidth: 0,
            			  // inline-block both sides: shrink to content (capped) so a
            			  // left-aligned broadcast message isn't always 70% wide.
            			  display: 'inline-block',
            			  textAlign: 'left',
            			  background: colors.bgLight, // same card background as responses
            			  border: '1px solid ' + colors.border,
            			  padding: alignRight ? '10px 14px' : '6px 10px',
            			  fontSize: alignRight ? fontSizes.base : fontSizes.sm,
			            
			            lineHeight: 1.5,
            			  color: colors.text,
            			  boxSizing: 'border-box',
            			}}
          >
            {/* Message box title, styled like the avatar: sender@target. A
                broadcast (no target) shows a "broadcast" label instead. */}
            			<div style={{ marginBottom: 4, display: 'flex', alignItems: 'baseline', gap: 6, justifyContent: alignRight ? 'flex-end' : 'flex-start', flexWrap: 'wrap' }}>
              {evt.target_worker_id ? (
                <>
                  <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontWeight: 'bold' }}>
                    {displayName(evt.worker_id)}
                  </span>
                  <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>@</span>
                  <span style={{ fontSize: fontSizes.sm, color: colors.accent }}>{displayName(evt.target_worker_id)}</span>
                </>
              ) : (
                <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontWeight: 'bold' }}>{t('talk.broadcast')}</span>
              )}
              {modeKey && (
                <span
                  title={t(`mode.${modeKey}.hint`)}
                  style={{ fontSize: fontSizes.xs, color: colors.textDimmed, userSelect: 'none' }}
                >
                  {t(`mode.${modeKey}`)}
                </span>
              )}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            <div className="md-content">
              {reminder && <SystemReminderBlock reminder={reminder} />}
              {content ? (
                <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{content}</Markdown>
              ) : null}
            </div>
            {parsed.attachments.length > 0 && (
              <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 6 }}>
                {parsed.attachments.map((a, i) => a.kind === 'image' ? (
                  <img
                    key={i}
                    src={`data:${a.mime};base64,${a.data}`}
                    alt={a.name || 'attachment'}
                    style={{ maxWidth: '100%', maxHeight: 220, borderRadius: 4, border: '1px solid ' + colors.border, display: 'block' }}
                  />
                ) : (
                  <span
                    key={i}
                    title={a.path}
                    style={{ fontSize: fontSizes.sm, color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 4, padding: '2px 8px' }}
                  >
                    {'\uD83D\uDCC4 ' + (a.name || a.path)}
                  </span>
                ))}
              </div>
            )}
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: alignRight ? 'right' : 'left' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title={t('talk.trace.tooltip')}
                >
                  {t('talk.trace')}
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // worker.abort — the human cancelled. Placement comes from the shared rule
    // (alignRight); only the look is its own: a dashed notice, not a card.
    if (evt.type === 'worker.abort') {
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
            <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
          </div>
          <div
            style={{
              maxWidth: alignRight ? '70%' : bubbleMax,
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              background: colors.bgLight,
              border: '1px solid ' + colors.border,
              padding: '8px 12px',
              fontSize: fontSizes.sm,
              color: colors.textDim,
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              {evt.target_worker_id && <span style={{ color: colors.textDimmed }}>to: {displayName(evt.target_worker_id)}</span>}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            <div style={{ color: colors.text, fontSize: fontSizes.sm, marginTop: 4 }}>worker.abort</div>
          </div>
        </div>
      )
      continue
    }

    // timer.reminder — a dedicated look for the timer's tick (⏰ purpose text),
    // placement from the shared rule like every other block.
    if (evt.type === 'timer.reminder') {
      let reminderText = (evt.payload?.text as string) || (evt.payload?.purpose as string) || ''
      if (!reminderText && evt.payload?.result) {
        const result = evt.payload.result
        if (typeof result === 'string') {
          try {
            const parsed = JSON.parse(result)
            reminderText = parsed.purpose || parsed.text || ''
          } catch {
            reminderText = result
          }
        } else if (typeof result === 'object') {
          reminderText = (result as any).purpose || (result as any).text || ''
        }
      }
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
          <div
            style={{
              maxWidth: alignRight ? '70%' : bubbleMax,
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              boxSizing: 'border-box',
              background: colors.bgLight,
              border: '1px solid ' + colors.border,
              padding: '10px 14px',
              fontSize: fontSizes.base,
              lineHeight: 1.5,
              color: colors.text,
            }}
          >
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              {evt.target_worker_id && <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>to: {displayName(evt.target_worker_id)}</span>}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>{formatTime(evt.timestamp)}</span>
            </div>
            {reminderText && <div style={{ color: colors.text }}>{reminderText}</div>}
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: 'right' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title={t('talk.trace.tooltip')}
                >
                  {t('talk.trace')}
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // timer.timeout — the timer reports a tool call timed out; same dedicated
    // look, shared placement rule.
    if (evt.type === 'timer.timeout') {
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
          <div
            style={{
              maxWidth: alignRight ? '70%' : bubbleMax,
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              boxSizing: 'border-box',
              background: colors.bgLight,
              border: '1px solid ' + colors.border,
              padding: '8px 12px',
              fontSize: fontSizes.sm,
              color: colors.textDim,
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <span style={{ color: colors.text }}>{t('talk.timeout')}</span>
              {evt.target_worker_id && <span style={{ color: colors.textDimmed }}>to: {displayName(evt.target_worker_id)}</span>}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
          </div>
        </div>
      )
      continue
    }

    // request.cancel — a cancelled request notice, styled like the tool blocks
    if (evt.type === 'request.cancel') {
      nodes.push(
        <div key={evt.id} style={{ maxWidth: bubbleMax, marginBottom: compactMode ? 8 : 12 }}>
<div style={{ border: '1px solid ' + colors.border, padding: tPad, fontSize: tFontSize, lineHeight: 1.5, color: colors.textDim }}>
              <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', rowGap: 4, columnGap: 8 }}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
                  <span style={{ width: 8, height: 8, borderRadius: 4, background: colors.textDim, flexShrink: 0, opacity: 0.5 }} />
                  <span>{t('talk.tool.cancelled')}</span>
                </span>
                {directionOf(evt) && (
                  <span style={{ ...itemSep, color: colors.textDimmed, whiteSpace: 'nowrap' }}>{directionOf(evt)}</span>
                )}
                <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto', whiteSpace: 'nowrap' }}>{formatTime(evt.timestamp)}</span>
              </div>
            </div>
        </div>
      )
      continue
    }

    // reason.interrupted — a reasoning round was preempted (new input, abort)
    if (evt.type === 'reason.interrupted') {
      const reason = (evt.payload?.reason as string) || ''
      const preserved = (evt.payload?.preserved_chars as number) || 0
      nodes.push(
        <div key={evt.id} style={{ maxWidth: bubbleMax, marginBottom: compactMode ? 8 : 12 }}>
          <div style={{ border: '1px solid ' + colors.border, padding: tPad, fontSize: tFontSize, lineHeight: 1.5, color: colors.textDim }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span style={{ width: 8, height: 8, borderRadius: 4, background: colors.textDim, flexShrink: 0, opacity: 0.5 }} />
                <span>{t('talk.reasoning.interrupted')}</span>
              </span>
              {reason && (
                <>
                  <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                  <span style={{ color: colors.textDimmed }}>{reason}</span>
                </>
              )}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            {preserved > 0 && (
              <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginTop: 4 }}>{t('talk.charsPreserved', { n: preserved })}</div>
            )}
          </div>
        </div>
      )
      continue
    }

    // Left-side events
    if (evt.type === 'reason.thinking') {
      nodes.push(
        <div key={evt.id + '-thinking-' + thinkingExpanded} style={{ maxWidth: bubbleMax }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
          <ThinkingBlock evt={evt} defaultExpanded={thinkingExpanded} compact={compactMode} />
        </div>
      )
      continue
    }
    if (evt.type === 'reason.response') {
      const ref = findReferencedInput(events, evt)
      nodes.push(
        <div key={evt.id} data-evt-id={evt.id} style={{ maxWidth: bubbleMax }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
          <ResponseBlock evt={evt} quotedText={ref?.text} quotedWorker={ref?.workerId} quotedEvtId={ref?.evtId} onQuoteClick={scrollToEvent} />
        </div>
      )
      continue
    }

    // Approval requests get a dedicated card: the boundary-expansion request
    // body plus, inline below it, quick approve/reject until a decision lands.
    // Expanded by default (the detail is the point of the card); the header
    // toggles the reason + payload panel just like the tool cards toggle
    // their arguments. The approve/reject actions stay visible either way.
    if (evt.type === 'approval.request') {
      const decision = decisionByRequestId[toolCallId(evt)]
      const approved = decision?.payload?.approved === true
      const note = typeof decision?.payload?.note === 'string' ? decision.payload.note : ''
      const isExpanded = !expandedContent.has(evt.id)
      const statusColor = decision
        ? approved ? colors.toolCompleted : colors.toolFailed
        : colors.toolRequested
      nodes.push(
        <div key={evt.id} data-evt-id={evt.id} style={{ marginTop: 16, marginBottom: compactMode ? 8 : 12, textAlign: alignRight ? 'right' : 'left' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
          <div className={!isExpanded ? 'block-card' : undefined} style={{ maxWidth: alignRight ? '70%' : bubbleMax, display: alignRight ? 'inline-block' : undefined, textAlign: 'left', boxSizing: 'border-box', border: '1px solid ' + colors.accent, padding: compactMode ? '4px 8px' : '6px 12px', fontSize: compactMode ? fontSizes.xs : fontSizes.base, lineHeight: 1.5, color: colors.textDim }}>
            <div onClick={() => toggleExpanded(evt.id)} style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ width: 8, height: 8, borderRadius: 4, background: statusColor, flexShrink: 0, opacity: 0.5 }} />
              <span style={{ color: colors.text, fontWeight: 600 }}>{t('talk.approval.title')}</span>
              {/* The source worker: who is asking for the boundary expansion. */}
              <span style={{ fontFamily: 'monospace', color: colors.text, fontSize: fontSizes.sm }}>{evt.worker_id}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>{isExpanded ? '▾' : '▸'}</span>
            </div>
            {/* Why this approval exists: keyed on the action, with a generic
                fallback so unknown future approval kinds stay explainable. */}
            {isExpanded && (
              <div style={{ marginTop: 6, fontSize: fontSizes.sm, color: colors.text }}>
                {t(evt.payload?.action === 'mount.add' ? 'approval.reason.mount.add' : 'approval.reason.generic')}
              </div>
            )}
            {/* The approval's data, rendered as JSON in a muted panel with the
                same soft-wrap toggle and fold affordances as the tool bodies.
                Hidden entirely when the payload is empty (legacy events carry
                no payload). */}
            {isExpanded && Object.keys(evt.payload ?? {}).length > 0 && (
              <div style={{ background: colors.bg, border: '1px solid ' + colors.borderLight, borderRadius: 2, padding: '6px 8px', marginTop: 8 }}>
                <CollapsibleCode code={JSON.stringify(evt.payload, null, 2)} language="json" />
              </div>
            )}
            {decision ? (
              <div style={{ marginTop: 8, fontSize: fontSizes.sm, color: approved ? colors.toolCompleted : colors.toolFailed }}>
                {approved ? t('talk.approval.approved') : t('talk.approval.rejected')}{note ? ` · ${note}` : ''}
              </div>
            ) : (
              onDecide && (
                <div style={{ display: 'flex', gap: 8, marginTop: 10 }}>
                  <span
                    onClick={() => onDecide(evt.id, true)}
                    className="btn-hover"
                    style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, color: colors.accent, borderRadius: 2, padding: '4px 12px', fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {t('talk.approval.approve')}
                  </span>
                  <span
                    onClick={() => onDecide(evt.id, false)}
                    className="btn-hover"
                    style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, color: colors.textDim, borderRadius: 2, padding: '4px 12px', fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {t('talk.approval.reject')}
                  </span>
                </div>
              )
            )}
          </div>
        </div>
      )
      continue
    }

    // Every other event renders as a card: the event type is the title and the
    // payload is the body. request_id is only a pairing key — when a
    // request.completed / failed / rejected answers this one, its body is
    // merged in below the arguments.
    {
      const callId = toolCallId(evt)
      const resultEvt = resultByRequestId[callId]

      const isExpanded = expandedContent.has(evt.id)
      const content = toolContent(evt, isExpanded)
      const mergedResult = resultEvt ? toolContent(resultEvt, isExpanded) : ''
      // The card shows the payload plus, when answered, the result body below.
      const displayContent = mergedResult
        ? (content ? content + '\n\n—— result ——\n\n' + mergedResult : mergedResult)
        : content
      const contentLen = toolContent(evt, false).length +
        (resultEvt ? toolContent(resultEvt, false).length : 0)
      const summary = toolSummary(evt)
      // Status colour: an answered card takes the outcome colour of its
      // result; one that carries a request_id is still awaiting its answer
      // (toolRequested); anything else was never a request.
      const statusColor = resultEvt
        ? resultEvt.type === 'request.completed' ? colors.toolCompleted
        : resultEvt.type === 'request.failed' ? colors.toolFailed
        : colors.textDim
        : evt.type === 'request.completed' ? colors.toolCompleted
        : evt.type === 'request.failed' ? colors.toolFailed
        : evt.type === 'request.rejected' ? colors.textDim
        : evt.request_id ? colors.toolRequested
        : colors.textDimmed

      const toolLabel = isToolResult(evt.type)
        ? evt.type === 'request.completed' ? t('talk.result')
        : evt.type === 'request.failed' ? t('talk.failed')
        : t('talk.rejected')
        : t('talk.call')

      // Streaming: accumulated request.progressed output shown live in the
      // card while the call is in flight (only before it resolves).
      const partialText = !resultEvt ? (toolPartials[callId] || '') : ''

      nodes.push(
        <div key={evt.id} style={{ marginBottom: compactMode ? 8 : 12, textAlign: alignRight ? 'right' : 'left' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
            </div>
          )}
          <div
            className={!isExpanded ? 'block-card' : undefined}
            style={{
              // Width lives on the card, not the wrapper: the wrapper stays
              // full-width so a right-aligned card hugs the true right edge,
              // while a left-aligned one keeps the plain block look. inline-
              // block only on the right, so the card shrink-wraps its content.
              maxWidth: alignRight ? '70%' : bubbleMax,
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              boxSizing: 'border-box',
              border: '1px solid ' + (isExpanded ? colors.accent : colors.border),
              padding: tPad,
              fontSize: tFontSize,
              lineHeight: 1.5,
              color: colors.textDim,
              background: isExpanded
                ? (dark ? 'rgba(60,120,180,0.06)' : 'rgba(60,120,180,0.04)')
                : undefined,
            }}
          >
            <div
              onClick={() => toggleExpanded(evt.id)}
              style={{ cursor: 'pointer', userSelect: 'none' }}
            >
              {isMobile ? (
                /* Mobile: title is just the label + a chevron; the metadata
                    moves to a dedicated second line once expanded. */
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
                    <span style={{ width: 8, height: 8, borderRadius: 4, background: statusColor, flexShrink: 0, opacity: 0.5 }} />
                    <span style={{ color: colors.textDim, fontSize: tFontSize }}>{toolLabel} {summary}</span>
                  </span>
                  <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto', whiteSpace: 'nowrap' }}>
                    {isExpanded ? '▾' : '▸'}
                  </span>
                </div>
              ) : (
                /* Desktop: original single-row title with all metadata. */
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ width: 8, height: 8, borderRadius: 4, background: statusColor, flexShrink: 0, opacity: 0.5 }} />
                    <span style={{ color: colors.textDim, fontSize: tFontSize }}>{toolLabel} {summary}</span>
                  </span>
                  {contentLen > 0 && (
                    <>
                      <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                      <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('thinking.chars', { n: contentLen })}</span>
                    </>
                  )}
                  {directionOf(evt, alignRight) && (
                    <>
                      <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                      <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{directionOf(evt, alignRight)}</span>
                    </>
                  )}
                  {isExpanded && contentLen > 0 && (
                    <>
                      <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                      <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('thinking.chars', { n: contentLen })}</span>
                    </>
                  )}
                  <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
                </div>
              )}
            </div>
            {isMobile && isExpanded && (
              <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', rowGap: 4, columnGap: 8, marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)'), fontSize: fontSizes.sm, color: colors.textDimmed }}>
                {directionOf(evt, alignRight) && (
                  <span style={{ whiteSpace: 'nowrap' }}>{directionOf(evt, alignRight)}</span>
                )}
                <span style={{ marginLeft: 'auto', whiteSpace: 'nowrap' }}>{formatTime(evt.timestamp)}</span>
              </div>
            )}
            {isExpanded && displayContent && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                {resultEvt ? (
                  <>
                    <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                      {t('talk.arguments')}
                    </div>
                    <CollapsibleCode code={content} language="json" />
                    <div style={{ margin: '10px 0 6px', height: 1, background: dark ? 'rgba(128,128,128,0.25)' : 'rgba(128,128,128,0.18)' }} />
                    <div style={{ fontSize: fontSizes.xs, color: resultEvt.type === 'request.failed' ? colors.toolFailed : resultEvt.type === 'request.rejected' ? colors.textDimmed : colors.toolCompleted, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                      {t('talk.result')}
                    </div>
                    <CollapsibleCode code={mergedResult} language="json" />
                  </>
                ) : (
                  <CollapsibleCode code={content} language="json" />
                )}
              </div>
            )}
            {partialText && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                <CollapsibleCode code={partialText} language="json" />
                <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, fontStyle: 'italic' }}>⏳ output streaming…</div>
              </div>
            )}
          </div>
        </div>
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
          onClick={(e) => {
            // Clicking blank space (the container itself) collapses any expanded
            // tool call/result blocks.
            if (e.target === scrollRef.current && expandedContent.size > 0) {
              setExpandedContent(new Set())
            }
          }}
          style={{ flex: 1, minWidth: 0, overflowY: 'auto', overflowX: 'hidden', padding: '0 24px 60px' }}
        >
        {nodes}
        {!responseOnly && streamingTraces.map(({ traceId, thinking, text, workerId, lastTs }) => {
          if (!thinking && !text) return null
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
                <WorkerBadge id={workerId} show={true} humanId={humanId} isReason={isReason} onMention={onMention} displayName={displayName} />
                <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, fontStyle: 'italic' }}>● streaming</span>
              </div>
              {thinking && (
                <ThinkingBlock evt={synthetic('reason.thinking', thinking)} defaultExpanded={thinkingExpanded} compact={compactMode} />
              )}
              {text && (
                <ResponseBlock evt={synthetic('reason.response', text)} />
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
