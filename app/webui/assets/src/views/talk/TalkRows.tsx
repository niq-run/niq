// TalkRows owns the per-row pipeline: the avatar-streak prepass, the
// right-alignment rule, the trailing run markers, and the per-event-type
// dispatch to the row components. TalkView hands it the scoped events plus the
// resolved per-request lookups; this module turns them into the row list.
import { useLayoutEffect, useRef, useState } from 'react'
import { fontSizes } from '../../theme'
import {
  isReasonBoundary, isToolEvent, isToolInvocation, isToolResult, toolCallId,
  getUsageParts, type UsageParts,
} from '../../components/talk-utils'
import type { EventPayload } from '../../types'
import WorkerBadge from './WorkerBadge'
import type { RowCtx } from './RowCtx'
import { AT_BOTTOM_PX, TALK_SCROLL_CLASS } from './useChatScroll'
import {
  InputRow, AbortRow, TimeoutRow,
  CancelRow, InterruptedRow, ThinkingRow, ResponseRow, ApprovalRow, ToolRow,
  RowBadge,
} from './rows'

export interface TalkRowsProps {
  // Events already scoped to the current talk filter (TalkView's memo).
  relevantEvents: EventPayload[]
  // The full event list — needed for response quotes (findReferencedInput).
  allEvents: EventPayload[]
  responseOnly: boolean
  talkWorkers: Set<string>
  resultByRequestId: Record<string, EventPayload>
  decisionByRequestId: Record<string, EventPayload>
  toolPartials: Record<string, string>
  ctx: RowCtx
}

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
function isRightAligned(evt: EventPayload, ctx: RowCtx, talkWorkers: Set<string>): boolean {
  // The current user's own messages always sit on the right, whether they
  // were directed at a reason worker or broadcast (e.g. a lone "@").
  if (evt.worker_id === ctx.humanId) return true
  const target = evt.target_worker_id
  if (!target || !ctx.isReason(target)) return false
  if (target === evt.worker_id) return false // self-directed: own tool call
  if (!ctx.isReason(evt.worker_id)) return true // (a) system -> reason
  return talkWorkers.size === 1 && talkWorkers.has(target) // (c)
}

// Trailing avatar stamped at the end of every left-side speaker run: a small
// avatar on the block's right edge — plus the context-usage label when the run
// ended on a reason.response carrying usage. Reuses WorkerBadge so the marker
// has the same left-click (mention) and right-click (detail / focus)
// affordances as the top badge.
function TrailMarker({ workerId, usage, ctx }: { workerId: string; usage?: UsageParts; ctx: RowCtx }) {
  const { colors, t, humanId, isReason, isPartner, onMention, onOpenDetail, onFocusWorker, displayName, bubbleMax, isMobile } = ctx
  const hasLeft = !!usage?.left
  const hasRight = !!usage?.right
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8, maxWidth: bubbleMax, margin: '2px 0 16px' }}>
      {usage && (hasLeft || hasRight) && (
        <span
          title={t('talk.usage.tooltip')}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 8,
            fontSize: fontSizes.xs, color: colors.textDimmed, userSelect: 'none',
            // On phone the trailing usage/context label is trimmed to a single
            // capped line with an ellipsis instead of forcing full width next to
            // the trailing avatar (the context line is informational, not
            // worth squeezing a narrow screen for).
            whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
            maxWidth: isMobile ? 'calc(100vw - 96px)' : '60%',
            flexShrink: 1, minWidth: 0,
          }}
        >
          {hasLeft && <span>{usage.left}</span>}
          {hasLeft && hasRight && (
            <span aria-hidden style={{ width: 1, height: '0.6em', background: colors.textDimmed, opacity: 0.45, alignSelf: 'center', flexShrink: 0 }} />
          )}
          {hasRight && <span>{usage.right}</span>}
        </span>
      )}
      <WorkerBadge
        id={workerId}
        show={true}
        small
        humanId={humanId}
        isReason={isReason}
        isPartner={isPartner}
        onMention={onMention}
        onOpenDetail={onOpenDetail}
        onFocusWorker={onFocusWorker}
        displayName={displayName}
      />
    </div>
  )
}

// Rows that are conversation content rather than process detail. Everything
// else — thinking, tool cards, interruptions, cancels — is what the collapse
// mode folds away, so a turn reads as input → [12 process events] → response.
const KEEP_VISIBLE = new Set(['worker.input', 'reason.response', 'approval.request', 'worker.abort', 'timer.timeout'])

// ProcessGroup is the collapsed stand-in for a run of process rows: one chip
// that names what was folded, expandable in place. It never hides the run's
// trailing speaker marker (that rides below the chip when collapsed), so the
// block still says whose output it was.
function ProcessGroup({ count, tools, thinking, other, open, badge, onToggle, children, ctx }: {
  count: number
  tools: number
  thinking: number
  other: number
  open: boolean
  // The run's opening speaker badge, shown only while folded: expanded, the
  // first row renders its own.
  badge: React.ReactNode
  onToggle: (chip: HTMLElement) => void
  children: React.ReactNode
  ctx: RowCtx
}) {
  const { colors, t } = ctx
  const detail = [
    tools > 0 ? t('talk.processGroup.tools', { n: tools }) : '',
    thinking > 0 ? t('talk.processGroup.thinking', { n: thinking }) : '',
    other > 0 ? t('talk.processGroup.other', { n: other }) : '',
  ].filter(Boolean).join(' · ')
  return (
    // Full-width wrapper, NOT capped at bubbleMax: the rows inside carry their
    // own maxWidth (a percentage), so capping the parent would shrink them to a
    // percentage of a percentage. Only the chip itself is capped.
    <div style={{ marginBottom: 12, width: '100%', minWidth: 0 }}>
      {badge}
      <div
        onClick={(e) => onToggle(e.currentTarget)}
        role="button"
        className="btn-hover"
        title={open ? t('talk.processGroup.collapse') : t('talk.processGroup.expand')}
        style={{
          display: 'flex', alignItems: 'center', gap: 8, width: '100%', maxWidth: ctx.bubbleMax, boxSizing: 'border-box',
          cursor: 'pointer', userSelect: 'none',
          background: colors.bgLight, border: '1px solid ' + colors.border, borderRadius: 2,
          padding: '4px 10px', fontSize: fontSizes.xs, lineHeight: '18px', color: colors.textDim,
        }}
      >
        {/* Drawn caret: the ▸/▾ glyphs sit off the text's optical centre in most
            UI fonts, which made the row look low. A border triangle is centred
            by construction. */}
        <span
          aria-hidden
          style={{
            flexShrink: 0, width: 0, height: 0,
            borderTop: '4px solid transparent', borderBottom: '4px solid transparent',
            borderLeft: '5px solid ' + colors.textDimmed,
            transform: open ? 'rotate(90deg)' : 'none',
            transition: 'transform 0.12s',
          }}
        />
        <span style={{ flexShrink: 0 }}>{t('talk.processGroup', { n: count })}</span>
        {detail && <span style={{ color: colors.textDimmed, marginLeft: 'auto', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{detail}</span>}
      </div>
      {open && <div style={{ marginTop: 6 }}>{children}</div>}
    </div>
  )
}

export default function TalkRows({ relevantEvents, allEvents, responseOnly, talkWorkers, resultByRequestId, decisionByRequestId, toolPartials, ctx }: TalkRowsProps) {
  // Which collapsed groups the reader has opened, keyed by the group's first
  // event id (stable across live appends). Purely local: reopening the view
  // starts folded again.
  const [openGroups, setOpenGroups] = useState<Record<string, boolean>>({})
  // Folding/unfolding changes the timeline height by however tall the group is
  // (a big tool list is thousands of pixels). Content above the viewport growing
  // leaves scrollTop untouched, so the reader is thrown to an earlier part of
  // the conversation. Remember the chip's screen position on click and restore
  // it after the commit: the row the finger is on stays where it was.
  const anchorTopRef = useRef<number | null>(null)
  const anchorElRef = useRef<HTMLElement | null>(null)
  // True when the reader was already at the bottom on click: the follow pin
  // owns the scroll then, and anchoring the chip would fight it (the view would
  // settle at the chip instead of the newest row).
  const anchorFollowRef = useRef(false)
  useLayoutEffect(() => {
    const top = anchorTopRef.current
    const chip = anchorElRef.current
    anchorTopRef.current = null
    if (top === null || !chip) return
    const scroller = chip.closest('.' + TALK_SCROLL_CLASS) as HTMLElement | null
    if (!scroller) return
    if (anchorFollowRef.current) return
    const delta = chip.getBoundingClientRect().top - top
    if (delta !== 0) scroller.scrollTop += delta
  })
  // Track the last DISPLAYED avatar's worker id. Avatars render for reason
  // workers and the human; hidden workers (workspace, timer, ...) don't reset
  // the streak, so a single speaker's avatar stays until the speaker switches.
  const rowFacts: { evt: EventPayload; alignRight: boolean; showBadge: boolean }[] = []
  let lastAvatarId = ''
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
    const alignRight = isRightAligned(evt, ctx, talkWorkers)
    const shouldShowAvatar = ctx.isReason(evt.worker_id) || evt.worker_id === ctx.humanId || alignRight
    const showBadge = shouldShowAvatar && evt.worker_id !== lastAvatarId &&
      evt.type !== 'reason.interrupted' && evt.type !== 'request.cancel'
    if (showBadge) lastAvatarId = evt.worker_id
    rowFacts.push({ evt, alignRight, showBadge })
  }

  // Trailing avatar: at the end of every speaker run (the last row sharing one
  // display avatar) stamp a small avatar — on the block's right edge — so the
  // reader always knows whose output this is without scrolling back to the top
  // badge. Always shown (no height heuristic), matching the compact footer look.
  // When the run ends on a reason.response that carries context usage, that
  // usage label rides along next to the trailing avatar.
  const trails: Record<number, string> = {}
  const trailUsage: Record<number, UsageParts> = {}
  {
    let runAvatar = ''
    let runUsage: UsageParts | null = null
    for (let i = 0; i < rowFacts.length; i++) {
      const f = rowFacts[i]
      if (f.showBadge) {
        runAvatar = f.evt.worker_id
        runUsage = null // new speaker: reset the run's usage label
      }
      if (f.evt.type === 'reason.response') runUsage = getUsageParts(f.evt, ctx.t)
      const runEnds = (i === rowFacts.length - 1) || rowFacts[i + 1].showBadge
      if (runEnds && runAvatar) {
        trails[i] = runAvatar
        if (runUsage && (runUsage.left || runUsage.right)) trailUsage[i] = runUsage
      }
    }
  }

  // Every run of consecutive process rows folds, down to a single row: with the
  // switch on, the very first thinking/tool event of a turn already arrives as
  // a folded chip. Waiting for a second row to appear would show that first row
  // expanded and then snap it shut a moment later while the turn streams in.
  const runEnd: Record<number, number> = {}
  if (ctx.collapseEvents) {
    for (let i = 0; i < rowFacts.length; ) {
      if (KEEP_VISIBLE.has(rowFacts[i].evt.type)) { i++; continue }
      let j = i
      while (j + 1 < rowFacts.length && !KEEP_VISIBLE.has(rowFacts[j + 1].evt.type)) j++
      runEnd[i] = j
      i = j + 1
    }
  }

  const renderRow = (i: number, suppressBadge = false): React.ReactNode => {
    const { evt, alignRight } = rowFacts[i]
    const showBadge = rowFacts[i].showBadge && !suppressBadge
    switch (evt.type) {
      case 'worker.input':
        return <InputRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />
      case 'worker.abort':
        return <AbortRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />
      case 'timer.timeout':
        return <TimeoutRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} ctx={ctx} />
      case 'request.cancel':
        return <CancelRow key={evt.id} evt={evt} ctx={ctx} />
      case 'reason.interrupted':
        return <InterruptedRow key={evt.id} evt={evt} ctx={ctx} />
      case 'reason.thinking':
        return <ThinkingRow key={evt.id} evt={evt} showBadge={showBadge} ctx={ctx} />
      case 'reason.response':
        return <ResponseRow key={evt.id} evt={evt} showBadge={showBadge} ctx={ctx} allEvents={allEvents} />
      case 'approval.request': {
        const decision = decisionByRequestId[toolCallId(evt)]
        return <ApprovalRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} decision={decision} ctx={ctx} />
      }
      default: {
        const callId = toolCallId(evt)
        const resultEvt = resultByRequestId[callId]
        const partial = toolPartials[callId] || ''
        return <ToolRow key={evt.id} evt={evt} alignRight={alignRight} showBadge={showBadge} resultEvt={resultEvt} partial={partial} ctx={ctx} />
      }
    }
  }

  // The speaker marker closes a run; it travels with whatever ends the run, so
  // a collapsed group keeps it too (below the chip) rather than swallowing it.
  const trailFor = (i: number): React.ReactNode => {
    const trail = trails[i]
    if (!trail || rowFacts[i].alignRight) return null
    return <TrailMarker key={rowFacts[i].evt.id + '-trail'} workerId={trail} usage={trailUsage[i]} ctx={ctx} />
  }

  const nodes: React.ReactNode[] = []
  for (let i = 0; i < rowFacts.length; i++) {
    const end = runEnd[i]
    if (end !== undefined) {
      const key = rowFacts[i].evt.id
      const open = !!openGroups[key]
      let tools = 0, thinking = 0, other = 0
      const inner: React.ReactNode[] = []
      for (let j = i; j <= end; j++) {
        switch (rowFacts[j].evt.type) {
          case 'reason.thinking':
            thinking++
            break
          case 'reason.interrupted':
          case 'request.cancel':
            other++
            break
          default:
            tools++
        }
        if (open) {
          inner.push(renderRow(j, j === i), trailFor(j))
        }
      }
      const head = rowFacts[i]
      nodes.push(
        <ProcessGroup
          key={key}
          count={end - i + 1}
          tools={tools}
          thinking={thinking}
          other={other}
          open={open}
          ctx={ctx}
          badge={<RowBadge ctx={ctx} workerId={head.evt.worker_id} showBadge={head.showBadge} extraPad />}
          onToggle={(chip) => {
            const scroller = chip.closest('.' + TALK_SCROLL_CLASS) as HTMLElement | null
            anchorFollowRef.current = !!scroller &&
              scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= AT_BOTTOM_PX
            anchorTopRef.current = chip.getBoundingClientRect().top
            anchorElRef.current = chip
            setOpenGroups((g) => ({ ...g, [key]: !g[key] }))
          }}
        >
          {inner}
        </ProcessGroup>,
      )
      if (!open) nodes.push(trailFor(end))
      i = end
      continue
    }
    nodes.push(renderRow(i), trailFor(i))
  }
  return <>{nodes}</>
}
