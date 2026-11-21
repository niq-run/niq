// TalkRows owns the per-row pipeline: the avatar-streak prepass, the
// right-alignment rule, the trailing run markers, and the per-event-type
// dispatch to the row components. TalkView hands it the scoped events plus the
// resolved per-request lookups; this module turns them into the row list.
import { Fragment, useLayoutEffect, useRef, useState } from 'react'
import { fontSizes } from '../../theme'
import {
  isReasonBoundary, isToolEvent, isToolInvocation, isToolResult, toolCallId,
  getUsageParts, type UsageParts,
} from '../../components/talk-utils'
import type { EventPayload } from '../../types'
import WorkerBadge from './WorkerBadge'
import type { RowCtx } from './RowCtx'
import { AT_BOTTOM_PX, TALK_SCROLL_CLASS } from './useChatScroll'
import { captureElementAnchor, settleScrollAnchor, type ScrollAnchor } from './scrollAnchor'
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

// A foldable run: consecutive process rows from ONE speaker on ONE side. Both
// halves matter — a right-aligned program event wedged between two left-side
// reason rows is its own block, not something the left chip can swallow, and a
// run never straddles a speaker change because its chip shows a single badge.
interface RowRun {
  rows: number[]
  ids: string[]
  workerId: string
  alignRight: boolean
  tools: number
  thinking: number
  other: number
}

// ProcessGroup is the folded stand-in for a run: one chip naming what was
// folded, expandable in place. Because a run is one speaker on one side, the
// chip's badge and its edge are unambiguous; the run's trailing marker still
// renders below the chip when folded, so the block keeps its speaker hint.
function ProcessGroup({ run, open, badge, onToggle, children, ctx }: {
  run: RowRun
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
    run.tools > 0 ? t('talk.processGroup.tools', { n: run.tools }) : '',
    run.thinking > 0 ? t('talk.processGroup.thinking', { n: run.thinking }) : '',
    run.other > 0 ? t('talk.processGroup.other', { n: run.other }) : '',
  ].filter(Boolean).join(' · ')
  return (
    // Full-width wrapper, NOT capped at bubbleMax: the rows inside carry their
    // own maxWidth (a percentage), so capping the parent would shrink them to a
    // percentage of a percentage. Only the chip itself is capped.
    <div style={{ marginBottom: 12, width: '100%', minWidth: 0 }}>
      {badge}
      <div
        // A block, not background: the timeline's click-away rule leaves
        // anything inside [data-block] alone, so folding a run never wipes the
        // tool card the reader had open.
        data-block=""
        onClick={(e) => onToggle(e.currentTarget)}
        role="button"
        className="btn-hover"
        title={open ? t('talk.processGroup.collapse') : t('talk.processGroup.expand')}
        style={{
          display: 'flex', alignItems: 'center', gap: 8, width: '100%', maxWidth: ctx.bubbleMax, boxSizing: 'border-box',
          // A right-side run's chip sits on the right, where its rows would be.
          marginLeft: run.alignRight ? 'auto' : undefined,
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
        <span style={{ flexShrink: 0 }}>{t('talk.processGroup', { n: run.rows.length })}</span>
        {detail && <span style={{ color: colors.textDimmed, marginLeft: 'auto', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{detail}</span>}
      </div>
      {open && <div style={{ marginTop: 6 }}>{children}</div>}
    </div>
  )
}

export default function TalkRows({ relevantEvents, allEvents, responseOnly, talkWorkers, resultByRequestId, decisionByRequestId, toolPartials, ctx }: TalkRowsProps) {
  // The rows the reader has unfolded. A set of event ids rather than one flag
  // per "group head": a run grows, shrinks and can gain a row at its head when
  // a late event lands, so its identity is the ids it contains — any of them
  // being here keeps the run open. Purely local; reopening the view folds again.
  const [openIds, setOpenIds] = useState<Set<string>>(new Set())
  // Folding/unfolding changes the timeline height by however tall the group is
  // (a big tool list is thousands of pixels), and the chip loses its badge above
  // it when it opens. Keep the chip the finger is on where it was: one row
  // anchor, settled once, with the same math the prepend uses (scrollAnchor.ts).
  // When the reader is already following the bottom, the follow pin owns the
  // scroll and no anchor is taken.
  const anchorRef = useRef<{ scroller: HTMLElement; anchor: ScrollAnchor } | null>(null)
  useLayoutEffect(() => {
    const pending = anchorRef.current
    if (!pending) return
    anchorRef.current = null
    settleScrollAnchor(pending.scroller, pending.anchor)
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
  // a collapsed run keeps it too (below the chip) rather than swallowing it.
  const trailFor = (i: number): React.ReactNode => {
    const trail = trails[i]
    if (!trail || rowFacts[i].alignRight) return null
    return <TrailMarker key={rowFacts[i].evt.id + '-trail'} workerId={trail} usage={trailUsage[i]} ctx={ctx} />
  }

  // The timeline is a list of blocks: a single row, or a foldable run. One pass
  // decides the split AND the run's summary, so the render loop never re-counts
  // — and the run's boundaries (`fold` below) are the whole grouping rule.
  const blocks: ({ kind: 'row'; index: number } | { kind: 'run'; run: RowRun })[] = []
  {
    let run: RowRun | null = null
    const flush = () => {
      if (run) blocks.push({ kind: 'run', run })
      run = null
    }
    for (let i = 0; i < rowFacts.length; i++) {
      const f = rowFacts[i]
      const fold = ctx.collapseEvents && !KEEP_VISIBLE.has(f.evt.type)
      if (!fold) {
        flush()
        blocks.push({ kind: 'row', index: i })
        continue
      }
      if (run && (run.alignRight !== f.alignRight || run.workerId !== f.evt.worker_id)) flush()
      if (!run) {
        run = {
          rows: [], ids: [], workerId: f.evt.worker_id, alignRight: f.alignRight,
          tools: 0, thinking: 0, other: 0,
        }
      }
      run.rows.push(i)
      run.ids.push(f.evt.id)
      if (f.evt.type === 'reason.thinking') run.thinking++
      else if (f.evt.type === 'reason.interrupted' || f.evt.type === 'request.cancel') run.other++
      else run.tools++
    }
    flush()
  }

  const runOpen = (run: RowRun) => run.ids.some(id => openIds.has(id))
  // Opening/closing writes every id in the run, so the state survives a run
  // that gains a row at its head (a late event) or at its tail (live output).
  const toggleRun = (run: RowRun) => setOpenIds(prev => {
    const next = new Set(prev)
    const open = run.ids.some(id => prev.has(id))
    for (const id of run.ids) {
      if (open) next.delete(id)
      else next.add(id)
    }
    return next
  })

  const nodes: React.ReactNode[] = []
  for (const block of blocks) {
    if (block.kind === 'row') {
      nodes.push(
        <Fragment key={rowFacts[block.index].evt.id}>
          {renderRow(block.index)}
          {trailFor(block.index)}
        </Fragment>,
      )
      continue
    }
    const { run } = block
    const head = run.rows[0]
    const open = runOpen(run)
    nodes.push(
      <ProcessGroup
        key={run.ids[0]}
        run={run}
        open={open}
        ctx={ctx}
        badge={<RowBadge ctx={ctx} workerId={run.workerId} showBadge={rowFacts[head].showBadge} alignRight={run.alignRight} extraPad />}
        onToggle={(chip) => {
          const scroller = chip.closest('.' + TALK_SCROLL_CLASS) as HTMLElement | null
          const following = !!scroller &&
            scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight <= AT_BOTTOM_PX
          const anchor = scroller && !following ? captureElementAnchor(chip, scroller) : null
          anchorRef.current = scroller && anchor ? { scroller, anchor } : null
          toggleRun(run)
        }}
      >
        {open && run.rows.map(j => (
          <Fragment key={rowFacts[j].evt.id}>
            {renderRow(j, j === head)}
            {trailFor(j)}
          </Fragment>
        ))}
      </ProcessGroup>,
    )
    if (!open) nodes.push(trailFor(run.rows[run.rows.length - 1]))
  }
  return <>{nodes}</>
}
