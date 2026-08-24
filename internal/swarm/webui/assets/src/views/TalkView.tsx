import { useMemo, useRef, useEffect, useLayoutEffect, useCallback, useState, type ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { vscDarkPlus } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { oneLight } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { useTheme, fontSizes } from '../theme'
import { makeMdComponents } from '../components/MarkdownComponents'
import ThinkingBlock from '../components/ThinkingBlock'
import WorkerUpdateBlock from '../components/WorkerUpdateBlock'
import ResponseBlock from '../components/ResponseBlock'
import TimerElapsedBlock from '../components/TimerElapsedBlock'
import {
  getInputText, isToolEvent, isReasonBoundary,
  toolContent, toolSummary, toolCallId,
  formatEventPayload, formatTime, truncate, findReferencedInput,
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
}

const inputRenderers: Record<string, React.FC<{evt: EventPayload; onTraceClick: (id: string) => void}>> = {
  'timer.elapsed': TimerElapsedBlock,
}

export default function TalkView({ events, talkWorkers, onTraceClick, onLoadMore, onMention, deliveries, humanId = 'default-hiw', workerTypes = {}, thinkingExpanded, compactMode, streamingMode, responseOnly }: TalkViewProps) {
  const { dark, colors } = useTheme()
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
      if (evt.type.startsWith('worker.') && evt.type !== 'worker.input' && evt.type !== 'worker.abort' && evt.type !== 'worker.update' && evt.type !== 'worker.updated') return false
      // Delta / partial events are never rendered as standalone rows. When
      // streaming mode is on they're consumed to build the streaming UI;
      // when off they're dropped entirely.
      if (evt.type === 'reason.thinking_delta' || evt.type === 'reason.text_delta' || evt.type === 'tool.partial') return false
      // Tool events belong to the reasoning conversation only when a reason
      // worker is a party (caller or target). Host lifecycle calls (hiw->host
      // suspend/resume), which involve no reason worker, stay out of talk.
      // The hasAnyReason guard defers filtering until we know the worker types,
      // so a not-yet-loaded list doesn't hide everything on first paint.
      if (isToolEvent(evt.type) && hasAnyReason && !involvesReason(evt)) return false
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

  // Active streaming traces: accumulate reason.*_delta by trace_id, drop a
  // trace once its final reason.thinking / reason.response arrives.
  const streamingTraces = useMemo(() => {
    if (!streamingMode) return [] as { traceId: string; thinking: string; text: string; workerId: string; lastTs: number }[]
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
  }, [events, streamingMode, talkWorkers, deliveries])

  // Live tool output: accumulate tool.partial by call_id. Only populated when
  // streaming mode is on; the partial text renders inside the matching
  // tool.requested card.
  const toolPartials = useMemo(() => {
    if (!streamingMode) return {} as Record<string, string>
    const map: Record<string, string> = {}
    for (const evt of events) {
      if (evt.type !== 'tool.partial') continue
      const callId = (evt.payload?.call_id as string) || ''
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
    // Once a tool has a terminal event (completed/failed/rejected), its result
    // card takes over and the live streaming content in the tool.requested
    // card is cleared.
    for (const evt of events) {
      if (isToolEvent(evt.type) && evt.type !== 'tool.requested') {
        const callId = (evt.payload?.call_id as string) || ''
        if (callId) delete map[callId]
      }
    }
    return map
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
        if (autoScrollRef.current && scrollRef.current) {
          scrollRef.current.scrollTo({ top: scrollRef.current.scrollHeight, behavior: animated ? 'smooth' : 'auto' })
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

  // Sentinel for auto-scroll-to-top loading
  const sentinel = onLoadMore && events.length > 0 ? (
    <div key="sentinel-top" ref={sentinelRef} style={{ height: 1 }} />
  ) : null
  if (sentinel) nodes.push(sentinel)

  for (const [i, evt] of relevantEvents.entries()) {
    if (isReasonBoundary(evt.type)) continue
    // Response-only mode: hide the intermediate process (thinking + tool calls,
    // including cancels).
    if (responseOnly && (evt.type === 'reason.thinking' || evt.type === 'reason.interrupted' || evt.type === 'tool.cancel' || isToolEvent(evt.type) || evt.type === 'worker.update' || evt.type === 'worker.updated')) continue

    // System events (timer/abort) always render their sender avatar, so they
    // must also advance the avatar streak — otherwise the next reason worker
    // event after a timer would wrongly see the same speaker and skip its avatar.
    const alwaysAvatar = evt.type === 'timer.reminder' || evt.type === 'timer.timeout' || evt.type === 'worker.abort'
    const shouldShowAvatar = alwaysAvatar || isReason(evt.worker_id) || evt.worker_id === humanId
    const showBadge = shouldShowAvatar && evt.worker_id !== lastAvatarId
    if (showBadge) lastAvatarId = evt.worker_id

    // worker.input
    if (evt.type === 'worker.input') {
      const alignRight = isRightAligned(evt)
      nodes.push(
        <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
              <WorkerBadge id={evt.worker_id} show={true} />
            </div>
          )}
          <div
            style={{
              maxWidth: '70%',
              display: alignRight ? 'inline-block' : undefined,
              textAlign: 'left',
              background: colors.bgLight, // same card background as responses
              border: '1px solid ' + colors.border,
              padding: alignRight ? '10px 14px' : '6px 10px',
              fontSize: alignRight ? fontSizes.base : fontSizes.sm,
              fontFamily: alignRight ? undefined : 'monospace',
              lineHeight: 1.5,
              color: colors.text,
            }}
          >
            {/* Message box title, styled like the avatar: sender@target. A
                broadcast (no target) shows a "broadcast" label instead. */}
            <div style={{ marginBottom: 4, display: 'flex', alignItems: 'baseline', gap: 6, width: '100%', justifyContent: alignRight ? 'flex-end' : 'flex-start', flexWrap: 'wrap' }}>
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
              <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(dark, colors)}>{getInputText(evt)}</Markdown>
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
              maxWidth: '70%',
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
              maxWidth: '70%',
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
              maxWidth: '70%',
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

    // tool.cancel — a cancelled tool call notice, styled like the tool blocks
    if (evt.type === 'tool.cancel') {
      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: compactMode ? 8 : 12 }}>
          <div style={{ border: '1px solid ' + colors.border, padding: tPad, fontSize: tFontSize, lineHeight: 1.5, color: colors.textDim }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span style={{ width: 8, height: 8, borderRadius: 4, background: colors.textDim, flexShrink: 0, opacity: 0.5 }} />
                <span>tool call cancelled</span>
              </span>
              {directionOf(evt) && (
                <>
                  <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
                  <span style={{ color: colors.textDimmed, fontFamily: 'monospace' }}>{directionOf(evt)}</span>
                </>
              )}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
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
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: compactMode ? 8 : 12 }}>
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
    if (isToolEvent(evt.type)) {
      const callId = toolCallId(evt)
      const isExpanded = expandedContent.has(callId)
      const content = toolContent(evt, isExpanded)
      const contentLen = toolContent(evt, false).length
      const summary = toolSummary(evt)
      const statusColor = evt.type === 'tool.requested' ? colors.toolRequested
        : evt.type === 'tool.completed' ? colors.toolCompleted
        : evt.type === 'tool.failed' ? colors.toolFailed
        : colors.textDim

      const toolLabel = evt.type === 'tool.requested' ? 'Tool Call'
        : evt.type === 'tool.completed' ? 'Tool Result'
        : evt.type === 'tool.failed' ? 'Tool Failed'
        : 'Tool Rejected'

      // Dim other tool events when one is expanded
      const isDimmed = anyToolExpanded && !isExpanded

      // Streaming: accumulated tool.partial output shown live in the
      // tool.requested card while the call is in flight.
      const partialText = evt.type === 'tool.requested' ? (toolPartials[callId] || '') : ''

      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: compactMode ? 8 : 12 }}>
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
              <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                  <span style={{ width: 8, height: 8, borderRadius: 4, background: isDimmed ? colors.textDimmed : statusColor, flexShrink: 0, opacity: 0.5 }} />
                  <span style={{ color: isDimmed ? colors.textDimmed : colors.textDim, fontSize: tFontSize }}>{toolLabel} {summary}</span>
                </span>
                {(evt.type === 'tool.completed' || evt.type === 'tool.requested') && contentLen > 0 && (
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
                <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
              </div>
            </div>
            {isExpanded && content && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                <SyntaxHighlighter
                  language="json"
                  style={dark ? vscDarkPlus : oneLight}
                  PreTag="div"
                  customStyle={{
                    margin: 0,
                    padding: '6px 8px',
                    borderRadius: 4,
                    fontSize: fontSizes.sm,
                    lineHeight: 1.4,
                    wordBreak: 'break-word',
                  }}
                >
                  {content}
                </SyntaxHighlighter>
              </div>
            )}
            {partialText && (
              <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
                <SyntaxHighlighter
                  language="json"
                  style={dark ? vscDarkPlus : oneLight}
                  PreTag="div"
                  customStyle={{
                    margin: 0,
                    padding: '6px 8px',
                    borderRadius: 4,
                    fontSize: fontSizes.sm,
                    lineHeight: 1.4,
                    wordBreak: 'break-word',
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
        <div key={evt.id + '-thinking-' + thinkingExpanded} style={{ maxWidth: '70%', opacity: isDimmed ? 0.35 : 1, transition: 'opacity 0.15s' }}>
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
        <div key={evt.id} style={{ maxWidth: '70%', opacity: isDimmed ? 0.35 : 1, transition: 'opacity 0.15s' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} />
            </div>
          )}
          <ResponseBlock evt={evt} quotedText={ref?.text} quotedWorker={ref?.workerId} />
        </div>
      )
    } else if (evt.type === 'worker.update' || evt.type === 'worker.updated') {
      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%' }}>
          {showBadge && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 16, marginBottom: 12 }}>
              <WorkerBadge id={evt.worker_id} show={true} />
            </div>
          )}
          <WorkerUpdateBlock evt={evt} compact={compactMode} />
        </div>
      )
    } else {
      nodes.push(
        <div key={evt.id} style={{ maxWidth: '70%', marginBottom: 12, fontSize: fontSizes.sm, color: colors.textDimmed }}>
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

  // Header: always visible, not in scroll area — shown even when empty.
  const header = (
    <div style={{ fontSize: fontSizes.xl, color: colors.text, padding: '0 24px', display: 'flex', alignItems: 'baseline', gap: 16, marginTop: 24, marginBottom: 12 }}>
      <strong>Talk</strong>
      <span style={{ fontSize: fontSizes.sm, color: colors.textMuted }}>
        watching <strong style={{ color: colors.textDim }}>{
          talkWorkers.size > 0
            ? `[${[...talkWorkers].join(', ')}]`
            : '[all workers]'
        }</strong>
      </span>
    </div>
  )

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
      <div ref={scrollRef} onScroll={handleScroll} style={{ flex: 1, overflowY: 'auto', overflowX: 'hidden', padding: '0 24px 60px' }}>
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
            <div key={`stream-${traceId}`} style={{ maxWidth: '70%', marginBottom: 12 }}>
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