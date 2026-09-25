// TalkRows owns the per-row pipeline: the avatar-streak prepass, the
// right-alignment rule, the trailing run markers, and the per-event-type
// dispatch to the row components. TalkView hands it the scoped events plus the
// resolved per-request lookups; this module turns them into the row list.
import { fontSizes } from '../../theme'
import {
  isReasonBoundary, isToolEvent, isToolInvocation, isToolResult, toolCallId,
  getUsageParts, type UsageParts,
} from '../../components/talk-utils'
import type { EventPayload } from '../../types'
import WorkerBadge from './WorkerBadge'
import type { RowCtx } from './RowCtx'
import {
  InputRow, AbortRow, TimerReminderRow, TimeoutRow,
  CancelRow, InterruptedRow, ThinkingRow, ResponseRow, ApprovalRow, ToolRow,
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
  const { colors, t, humanId, isReason, onMention, onOpenDetail, onFocusWorker, displayName, bubbleMax } = ctx
  const hasLeft = !!usage?.left
  const hasRight = !!usage?.right
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8, maxWidth: bubbleMax, margin: '2px 0 16px' }}>
      {usage && (hasLeft || hasRight) && (
        <span
          title={t('talk.usage.tooltip')}
          style={{ display: 'inline-flex', alignItems: 'center', gap: 8, fontSize: fontSizes.xs, color: colors.textDimmed, userSelect: 'none', whiteSpace: 'nowrap' }}
        >
          {hasLeft && <span>{usage.left}</span>}
          {hasLeft && hasRight && (
            <span aria-hidden style={{ width: 1, height: '0.6em', background: colors.textDimmed, opacity: 0.45, alignSelf: 'center' }} />
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
        onMention={onMention}
        onOpenDetail={onOpenDetail}
        onFocusWorker={onFocusWorker}
        displayName={displayName}
      />
    </div>
  )
}

export default function TalkRows({ relevantEvents, allEvents, responseOnly, talkWorkers, resultByRequestId, decisionByRequestId, toolPartials, ctx }: TalkRowsProps) {
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

  const nodes: React.ReactNode[] = []
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
        nodes.push(<ResponseRow key={evt.id} evt={evt} showBadge={showBadge} ctx={ctx} allEvents={allEvents} />)
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
    // Stamp a small trailing avatar on the LEFT block's right edge at the end
    // of every speaker run. Right-aligned messages already carry their avatar
    // on the right, so they don't need the extra marker.
    const trail = trails[i]
    if (trail && !alignRight) {
      nodes.push(<TrailMarker key={evt.id + '-trail'} workerId={trail} usage={trailUsage[i]} ctx={ctx} />)
    }
  }
  return <>{nodes}</>
}
