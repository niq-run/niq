import { useMemo, useRef, useEffect, useLayoutEffect, useCallback, useState, type ReactNode, type CSSProperties } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { vscDarkPlus } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { useTheme, fontSizes } from '../theme'
import { makeMdComponents } from '../components/MarkdownComponents'
import ThinkingBlock from '../components/ThinkingBlock'
import ResponseBlock from '../components/ResponseBlock'
import TimerElapsedBlock from '../components/TimerElapsedBlock'
import SystemReminderBlock from '../components/SystemReminderBlock'
import {
  getInputText, isToolEvent, isToolInvocation, isReasonBoundary,
  toolContent, toolSummary, toolCallId,
  formatEventPayload, formatTime, truncate, findReferencedInput, splitSystemReminder,
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

const inputRenderers: Record<string, React.FC<{evt: EventPayload; onTraceClick: (id: string) => void}>> = {
  'timer.elapsed': TimerElapsedBlock,
}

export default function TalkView({ events, talkWorkers, onTraceClick, onLoadMore, onMention, deliveries, humanId = 'webui-hiw', workerTypes = {}, thinkingExpanded, compactMode, streamingMode, responseOnly, isMobile }: TalkViewProps) {
  const { dark, colors } = useTheme()
  // Left-side bubbles are wider on phones (90%) and keep the original 70% on
  // desktop.
  const bubbleMax = isMobile ? '90%' : '70%'
  // Only reason workers (the conversation partners) get a standalone avatar
  // row; other workers' events carry their worker ID inline in the block title.
  const isReason = (wid: string) => workerTypes[wid] === 'reason'
  // Convert the human worker's id into a friendlier "you" for display.
  const displayName = (wid?: string) => (wid && wid === humanId ? 'you' : wid ?? '')
  // Direction of the worker identity in a block title, relative to the reason
  // worker: "to X" for events the reason worker sends, "from: X" for events it
  // receives from another worker.
  const directionOf = (evt: EventPayload): string => {
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
  const [expandedContent, setExpandedContent] = useState<Set<string>>(new Set())
  const [wrapCode, setWrapCode] = useState(true) // soft-wrap tool JSON
  const prevEventCount = useRef(0)
  const prevStreamLen = useRef(0)
  const hasInitialScrolled = useRef(false)
  // The first ~second after mounting is the initial population (e.g. after
  // switching to the Talk view): scrolls are instant so the page does not
  // animate from top to bottom. Real-time pushes after that smooth-scroll.
  const mountedAt = useRef(Date.now())

  const toggleToolContent = (callId: string) => {
    setExpandedContent(prev => {
      const next = new Set(prev)
      if (next.has(callId)) next.delete(callId)
      else next.add(callId)
      return next
    })
  }

  // A reason worker is a party (caller/target/recipient) to the event.
  const involvesReason = (evt: EventPayload): boolean => {
    const reason = (id?: string) => (id ? workerTypes[id] === 'reason' : false)
    return reason(evt.worker_id) || reason(evt.target_worker_id) ||
      (evt.recipients || []).some(id => reason(id))
  }
  const hasAnyReason = Object.values(workerTypes).some(t => t === 'reason')

  // Filter events by selected workers. When no workers selected, show all.
  const relevantEvents = useMemo(() => {
    return events.filter(evt => {
      if (evt.type.startsWith('worker.') && evt.type !== 'worker.input' && evt.type !== 'worker.abort') return false
      // Internal meta plumbing (context.compress / provider.*) is never a
      // talk row — the LLM-facing tool card (invocation + request.*) is its
      // user-facing representation.
      if (evt.type === 'context.compress' || evt.type === 'context.rotate' || evt.type.startsWith('provider.')) return false
      // Delta / partial events are never rendered as standalone rows. When
      // streaming mode is on they're consumed to build the streaming UI;
      // when off they're dropped entirely.
      if (evt.type === 'reason.thinking_delta' || evt.type === 'reason.text_delta' || evt.type === 'request.progressed') return false
      // Tool events belong to the reasoning conversation only when a reason
      // worker is a party (caller or target). Host lifecycle calls (hiw->host
      // suspend/resume), which involve no reason worker, stay out of talk.
      // The hasAnyReason guard defers filtering until we know the worker types,
      // so a not-yet-loaded list doesn't hide everything on first paint.
      if ((isToolEvent(evt.type) || isToolInvocation(evt)) && hasAnyReason && !involvesReason(evt)) return false
      if (talkWorkers.size === 0) return true // show all when none selected
      const recipients = deliveries[evt.id] || evt.recipients
      if (evt.type === 'worker.input') {
        if (talkWorkers.has(evt.target_worker_id)) return true
        if (talkWorkers.has(evt.worker_id)) return true
        if (recipients && recipients.some(r => talkWorkers.has(r))) return true
        return false
      }
      if (talkWorkers.has(evt.worker_id)) return true
      if (talkWorkers.has(evt.target_worker_id)) return true
      if (recipients && recipients.some(r => talkWorkers.has(r))) return true
      return false
    })
  }, [events, talkWorkers, deliveries, workerTypes])

  // request_id → terminal result event. A tool invocation is rendered with its
  // matched result merged into the same card (the result row is then skipped),
  // so the result's identity comes from the invocation, not from a name field.
  const resultByRequestId = useMemo(() => {
    const m: Record<string, EventPayload> = {}
    for (const evt of events) {
      if ((evt.type === 'request.completed' || evt.type === 'request.failed' || evt.type === 'request.rejected') && evt.request_id) {
        m[evt.request_id] = evt
      }
    }
    return m
  }, [events])

  // Streaming content (reason.*_delta and request.progressed) is recomputed
  // on a 2s tick rather than per SSE event, so rapid delta bursts coalesce
  // into a single repaint instead of re-rendering on every event.
  const eventsRef = useRef(events)
  eventsRef.current = events
  const [streamingTraces, setStreamingTraces] = useState<StreamTrace[]>([])
  const [toolPartials, setToolPartials] = useState<Record<string, string>>({})

  useEffect(() => {
    const compute = () => {
      if (!streamingMode) {
        setStreamingTraces([])
        setToolPartials({})
        return
      }
      const evs = eventsRef.current
      setStreamingTraces(computeStreamingTraces(evs, talkWorkers, deliveries))
      setToolPartials(computeToolPartials(evs, talkWorkers, deliveries))
    }
    compute()
    const t = setInterval(compute, 2000)
    return () => clearInterval(t)
  }, [streamingMode, talkWorkers, deliveries])


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

  // ── Worker name label (avatar) ──
  // Only reason workers are mentionable: they get the hover underline and a
  // click-to-@ action. Other speakers' avatars are plain labels (no underline,
  // no hover, no @). The human worker id renders as "you".
  function WorkerBadge({ id, show }: { id: string; show: boolean }) {
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
        {mentionable && <span className="badge-tip">click to mention</span>}
      </span>
    )
  }

  const nodes: React.ReactNode[] = []

  // Track whether any tool call content is expanded (for dimming)
  const anyToolExpanded = expandedContent.size > 0

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

    // A terminal result whose invocation is in the stream is merged into that
    // invocation's card and never rendered as its own row. Skip it before the
    // avatar bookkeeping below: its worker_id is the callee (lark, host, ...)
    // and letting it advance the streak would break it invisibly, making the
    // real speaker's next event re-show the avatar.
    if (!isToolInvocation(evt) && isToolEvent(evt.type) && resultByRequestId[toolCallId(evt)] === evt) continue

    // System events (timer/abort) always render their sender avatar, so they
    // must also advance the avatar streak — otherwise the next reason worker
    // event after a timer would wrongly see the same speaker and skip its avatar.
    	const alwaysAvatar = evt.type === 'timer.reminder' || evt.type === 'timer.timeout' || evt.type === 'worker.abort'
    	// Show a worker-name avatar for reason workers, the human, system events,
    	// and any right-aligned message (e.g. an external worker like the lark
    	// bridge speaking to a reason worker) so its identity is visible.
    	const shouldShowAvatar = alwaysAvatar || isReason(evt.worker_id) || evt.worker_id === humanId || isRightAligned(evt)
    // Notice rows that render without an avatar (interrupted / cancelled /
    // timer-elapsed) must not consume the streak either: nothing identifying
    // the speaker is displayed, so a streak they set would be invisible.
    const showBadge = shouldShowAvatar && evt.worker_id !== lastAvatarId &&
      evt.type !== 'reason.interrupted' && evt.type !== 'request.cancel' && evt.type !== 'timer.elapsed'
    if (showBadge) lastAvatarId = evt.worker_id

    // worker.input
    if (evt.type === 'worker.input') {
      const alignRight = isRightAligned(evt)
      const { reminder, content } = splitSystemReminder(getInputText(evt))
      nodes.push(
        		<div key={evt.id} data-evt-id={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
        		  {showBadge && (
        			<div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} />
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
            			  fontFamily: alignRight ? undefined : 'monospace',
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
                  <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontWeight: 'bold', fontFamily: 'monospace' }}>
                    {displayName(evt.worker_id)}
                  </span>
                  <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>@</span>
                  <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontFamily: 'monospace' }}>{displayName(evt.target_worker_id)}</span>
                </>
              ) : (
                <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontWeight: 'bold', fontFamily: 'monospace' }}>broadcast</span>
              )}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            <div className="md-content">
              {reminder && <SystemReminderBlock reminder={reminder} />}
              {content ? (
                <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{content}</Markdown>
              ) : null}
            </div>
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: alignRight ? 'right' : 'left' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title="View all events in this trace"
                >
                  trace
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // worker.abort — the human cancelled; a bubble shown right (if addressed
    // to the selected reason worker) or left
    if (evt.type === 'worker.abort') {
      const alignRight = isRightAligned(evt)
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
            <WorkerBadge id={evt.worker_id} show={true} />
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
              {evt.target_worker_id && <span style={{ color: colors.textDimmed, fontFamily: 'monospace' }}>to: {displayName(evt.target_worker_id)}</span>}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            <div style={{ color: colors.text, fontSize: fontSizes.sm, marginTop: 4 }}>worker.abort</div>
          </div>
        </div>
      )
      continue
    }

    // timer.reminder — a notice bubble shown right (if addressed to the
    // selected reason worker) or left
    if (evt.type === 'timer.reminder') {
      const alignRight = isRightAligned(evt)
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
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
            <WorkerBadge id={evt.worker_id} show={true} />
          </div>
          <div
            style={{
              maxWidth: alignRight ? '70%' : bubbleMax,
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              background: colors.bgLight,
              border: '1px solid ' + colors.border,
              padding: '10px 14px',
              fontSize: fontSizes.base,
              lineHeight: 1.5,
              color: colors.text,
            }}
          >
            <div style={{ fontSize: fontSizes.sm, color: colors.textDim, marginBottom: 4, display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', justifyContent: 'flex-end', width: '100%' }}>
              {evt.target_worker_id && <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm, fontFamily: 'monospace' }}>to: {displayName(evt.target_worker_id)}</span>}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>{formatTime(evt.timestamp)}</span>
            </div>
            {reminderText && <div style={{ color: colors.text }}>{reminderText}</div>}
            {evt.trace_id && (
              <div style={{ marginTop: 6, textAlign: 'right' }}>
                <span
                  onClick={() => onTraceClick(evt.trace_id!)}
                  style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }}
                  title="View all events in this trace"
                >
                  trace
                </span>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // timer.timeout — notice that a tool call timed out; right if addressed to
    // the selected reason worker, otherwise left
    if (evt.type === 'timer.timeout') {
      const alignRight = isRightAligned(evt)
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          {/* Sender avatar: always show the timer worker for this system event */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
            <WorkerBadge id={evt.worker_id} show={true} />
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
              <span style={{ color: colors.text }}>timeout</span>
              {evt.target_worker_id && <span style={{ color: colors.textDimmed, fontFamily: 'monospace' }}>to: {displayName(evt.target_worker_id)}</span>}
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
                  <span>tool call cancelled</span>
                </span>
                {directionOf(evt) && (
                  <span style={{ ...itemSep, color: colors.textDimmed, fontFamily: 'monospace', whiteSpace: 'nowrap' }}>{directionOf(evt)}</span>
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
                <span>reasoning interrupted</span>
              </span>
              {reason && (
                <>
                  <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                  <span style={{ color: colors.textDimmed, fontFamily: 'monospace' }}>{reason}</span>
                </>
              )}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
            {preserved > 0 && (
              <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginTop: 4 }}>{preserved} chars preserved</div>
            )}
          </div>
        </div>
      )
      continue
    }

    // Input renderers (right-side events)
    const InputRenderer = inputRenderers[evt.type]
    if (InputRenderer) {
      nodes.push(<InputRenderer key={evt.id} evt={evt} onTraceClick={onTraceClick} />)
      continue
    }

    // Tool events: render individually in natural order
    if (isToolEvent(evt.type) || isToolInvocation(evt)) {
      const callId = toolCallId(evt)
      const isInvocation = isToolInvocation(evt)
      const resultEvt = isInvocation ? resultByRequestId[callId] : undefined

      const isExpanded = expandedContent.has(callId)
      const content = toolContent(evt, isExpanded)
      const mergedResult = resultEvt ? toolContent(resultEvt, isExpanded) : ''
      // Invocation card shows the arguments plus, when answered, the merged
      // result body below it.
      const displayContent = isInvocation && mergedResult
        ? (content ? content + '\n\n—— result ——\n\n' + mergedResult : mergedResult)
        : content
      const contentLen = (isInvocation && mergedResult
        ? content + '\n\n—— result ——\n\n' + mergedResult
        : toolContent(evt, false)).length
      const summary = toolSummary(evt)
      // Code-block styling for the tool bodies. react-syntax-highlighter puts
      // its own white-space on the <code> element, so the wrap toggle must be
      // applied there too, not just on the <pre> via customStyle.
      const hlStyle = dark ? vscDarkPlus : oneLight
      const codeWrapStyle: React.CSSProperties = {
        whiteSpace: wrapCode ? 'pre-wrap' : 'pre',
        wordBreak: wrapCode ? 'break-word' : 'normal',
      }
      // Status colour: an answered invocation takes the outcome colour
      // (completed/failed/rejected); an in-flight one stays toolRequested.
      const statusColor = isInvocation
        ? resultEvt
          ? resultEvt.type === 'request.completed' ? colors.toolCompleted
          : resultEvt.type === 'request.failed' ? colors.toolFailed
          : colors.textDim
          : colors.toolRequested
        : evt.type === 'request.completed' ? colors.toolCompleted
        : evt.type === 'request.failed' ? colors.toolFailed
        : colors.textDim

      const toolLabel = isInvocation ? 'Tool Call'
        : evt.type === 'request.completed' ? 'Tool Result'
        : evt.type === 'request.failed' ? 'Tool Failed'
        : 'Tool Rejected'

      // Dim other tool events when one is expanded
      const isDimmed = anyToolExpanded && !isExpanded

      // Streaming: accumulated request.progressed output shown live in the
      // invocation card while the call is in flight (only before it resolves).
      const partialText = isInvocation && !resultEvt ? (toolPartials[callId] || '') : ''

      nodes.push(
        <div key={evt.id} style={{ maxWidth: bubbleMax, marginBottom: compactMode ? 8 : 12 }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} />
            </div>
          )}
          <div
            className={!isExpanded ? 'block-card' : undefined}
            style={{
              border: '1px solid ' + colors.border,
              padding: tPad,
              fontSize: tFontSize,
              lineHeight: 1.5,
              color: colors.textDim,
              opacity: isDimmed ? 0.35 : 1,
              transition: 'opacity 0.15s',
            }}
          >
            <div
              onClick={() => toggleToolContent(callId)}
              style={{ cursor: 'pointer', userSelect: 'none' }}
            >
              {isMobile ? (
                /* Mobile: title is just the label + a chevron; the metadata
                    moves to a dedicated second line once expanded. */
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
                    <span style={{ width: 8, height: 8, borderRadius: 4, background: isDimmed ? colors.textDimmed : statusColor, flexShrink: 0, opacity: 0.5 }} />
                    <span style={{ color: isDimmed ? colors.textDimmed : colors.textDim, fontSize: tFontSize }}>{toolLabel} {summary}</span>
                  </span>
                  <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto', whiteSpace: 'nowrap' }}>
                    {isExpanded ? '▾' : '▸'}
                  </span>
                </div>
              ) : (
                /* Desktop: original single-row title with all metadata. */
                <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                  <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ width: 8, height: 8, borderRadius: 4, background: isDimmed ? colors.textDimmed : statusColor, flexShrink: 0, opacity: 0.5 }} />
                    <span style={{ color: isDimmed ? colors.textDimmed : colors.textDim, fontSize: tFontSize }}>{toolLabel} {summary}</span>
                  </span>
                  {(evt.type === 'request.completed' || isToolInvocation(evt)) && contentLen > 0 && (
                    <>
                      <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                      <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{contentLen} chars</span>
                    </>
                  )}
                  {directionOf(evt) && (
                    <>
                      <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                      <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm, fontFamily: 'monospace' }}>{directionOf(evt)}</span>
                    </>
                  )}
                  {isExpanded && contentLen > 0 && (
                    <>
                      <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                      <span
                        onClick={(e) => { e.stopPropagation(); setWrapCode(v => !v) }}
                        title="toggle soft wrap"
                        style={{ cursor: 'pointer', color: colors.accentDim, fontSize: fontSizes.sm, textDecoration: wrapCode ? undefined : 'underline dotted' }}
                      >
                        {wrapCode ? 'wrap' : 'no-wrap'}
                      </span>
                    </>
                  )}
                  <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
                </div>
              )}
            </div>
            {isMobile && isExpanded && (
              <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', rowGap: 4, columnGap: 8, marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)'), fontSize: fontSizes.sm, color: colors.textDimmed }}>
                {directionOf(evt) && (
                  <span style={{ whiteSpace: 'nowrap' }}>{directionOf(evt)}</span>
                )}
                <span style={{ marginLeft: 'auto', whiteSpace: 'nowrap' }}>{formatTime(evt.timestamp)}</span>
              </div>
            )}
            {isExpanded && displayContent && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                {isInvocation && resultEvt ? (
                  <>
                    <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                      arguments
                    </div>
                    <SyntaxHighlighter
                      language="json"
                      style={hlStyle}
                      codeTagProps={{ style: codeWrapStyle }}
                      PreTag="div"
                      customStyle={{
                        margin: 0,
                        padding: '6px 8px',
                        borderRadius: 4,
                        fontSize: fontSizes.sm,
                        lineHeight: 1.4,
                        whiteSpace: wrapCode ? 'pre-wrap' : 'pre',
                        wordBreak: wrapCode ? 'break-word' : 'normal',
                        maxHeight: 320,
                        overflowY: 'auto',
                        overflowX: 'auto',
                      }}
                    >
                      {content}
                    </SyntaxHighlighter>
                    <div style={{ margin: '10px 0 6px', height: 1, background: dark ? 'rgba(128,128,128,0.25)' : 'rgba(128,128,128,0.18)' }} />
                    <div style={{ fontSize: fontSizes.xs, color: resultEvt.type === 'request.failed' ? colors.toolFailed : resultEvt.type === 'request.rejected' ? colors.textDimmed : colors.toolCompleted, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                      result
                    </div>
                    <SyntaxHighlighter
                      language="json"
                      style={hlStyle}
                      codeTagProps={{ style: codeWrapStyle }}
                      PreTag="div"
                      customStyle={{
                        margin: 0,
                        padding: '6px 8px',
                        borderRadius: 4,
                        fontSize: fontSizes.sm,
                        lineHeight: 1.4,
                        whiteSpace: wrapCode ? 'pre-wrap' : 'pre',
                        wordBreak: wrapCode ? 'break-word' : 'normal',
                        maxHeight: 320,
                        overflowY: 'auto',
                        overflowX: 'auto',
                      }}
                    >
                      {mergedResult}
                    </SyntaxHighlighter>
                  </>
                ) : (
                  <SyntaxHighlighter
                    language="json"
                    style={hlStyle}
                    codeTagProps={{ style: codeWrapStyle }}
                    PreTag="div"
                    customStyle={{
                      margin: 0,
                      padding: '6px 8px',
                      borderRadius: 4,
                      fontSize: fontSizes.sm,
                      lineHeight: 1.4,
                      whiteSpace: wrapCode ? 'pre-wrap' : 'pre',
                      wordBreak: wrapCode ? 'break-word' : 'normal',
                      maxHeight: 320,
                      overflowY: 'auto',
                      overflowX: 'auto',
                    }}
                  >
                    {displayContent}
                  </SyntaxHighlighter>
                )}
              </div>
            )}
            {partialText && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                <SyntaxHighlighter
                  language="json"
                  style={hlStyle}
                  codeTagProps={{ style: codeWrapStyle }}
                  PreTag="div"
                  customStyle={{
                    margin: 0,
                    padding: '6px 8px',
                    borderRadius: 4,
                    fontSize: fontSizes.sm,
                    lineHeight: 1.4,
                    whiteSpace: wrapCode ? 'pre-wrap' : 'pre',
                    wordBreak: wrapCode ? 'break-word' : 'normal',
                    maxHeight: 320,
                    overflowY: 'auto',
                    overflowX: 'auto',
                  }}
                >
                  {partialText}
                </SyntaxHighlighter>
                <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, fontStyle: 'italic' }}>⏳ output streaming…</div>
              </div>
            )}
          </div>
        </div>
      )
      continue
    }

    // Left-side events
    if (evt.type === 'reason.thinking') {
      const isDimmed = anyToolExpanded
      nodes.push(
        <div key={evt.id + '-thinking-' + thinkingExpanded} style={{ maxWidth: bubbleMax, opacity: isDimmed ? 0.35 : 1, transition: 'opacity 0.15s' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} />
            </div>
          )}
          <ThinkingBlock evt={evt} defaultExpanded={thinkingExpanded} compact={compactMode} />
        </div>
      )
    } else if (evt.type === 'reason.response') {
      const ref = findReferencedInput(events, evt)
      const isDimmed = anyToolExpanded
    		  nodes.push(
    			<div key={evt.id} data-evt-id={evt.id} style={{ maxWidth: bubbleMax, opacity: isDimmed ? 0.35 : 1, transition: 'opacity 0.15s' }}>
    			  {showBadge && (
    				<div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
    				  <WorkerBadge id={evt.worker_id} show={true} />
    				</div>
    			  )}
    			  <ResponseBlock evt={evt} quotedText={ref?.text} quotedWorker={ref?.workerId} quotedEvtId={ref?.evtId} onQuoteClick={scrollToEvent} />
    			</div>
    		  )
    } else {
      nodes.push(
        <div key={evt.id} style={{ maxWidth: bubbleMax, marginBottom: 12, fontSize: fontSizes.sm, color: colors.textDimmed }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} />
            </div>
          )}
          <div style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>
            {evt.type}
            {directionOf(evt) && <span style={{ fontFamily: 'monospace' }}> {directionOf(evt)}</span>}
          </div>
          {formatEventPayload(evt)}
        </div>
      )
    }
  }

  // Header: always visible, not in scroll area — shown even when empty. On
  // mobile the app-level top bar (hamburger + view label) already heads the
  // page, so this in-view header is skipped to avoid a double header.
  const header = !isMobile ? (
    <>
      <div style={{ fontSize: fontSizes.xl, color: colors.text, padding: '0 24px', display: 'flex', alignItems: 'baseline', gap: 16, marginTop: 24 }}>
        <strong>Talk</strong>
        <span style={{ fontSize: fontSizes.sm, color: colors.textMuted }}>
          watching <strong style={{ color: colors.textDim }}>{
            talkWorkers.size > 0
              ? `[${[...talkWorkers].join(', ')}]`
              : '[all workers]'
          }</strong>
        </span>
      </div>
      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px 0 0' }} />
    </>
  ) : null

  if (nodes.length === 0 && streamingTraces.length === 0) {
    const label = talkWorkers.size > 0
      ? [...talkWorkers].join(', ')
      : 'all workers'
    return (
      <>
        {header}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', height: '100%', color: colors.textDimmed }}>
          <p style={{ fontSize: fontSizes.md }}>No messages yet. Watching <strong style={{ color: colors.textDim }}>{label}</strong>.</p>
        </div>
      </>
    )
  }

  return (
    <>
      {header}
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
        style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '0 24px 60px' }}
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
                <WorkerBadge id={workerId} show={true} />
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
    </>
  )
}

// Also re-export helpers for consumers that may need them
export { formatTime, truncate }