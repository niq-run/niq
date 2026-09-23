// The live streaming tail: in-flight reason traces rendered from the delta
// accumulators (see streaming.ts) as synthetic Thinking/Response blocks.
import type { ReactNode } from 'react'
import { fontSizes } from '../../theme'
import ThinkingBlock from '../../components/ThinkingBlock'
import ResponseBlock from '../../components/ResponseBlock'
import type { EventPayload } from '../../types'
import WorkerBadge from './WorkerBadge'
import type { RowCtx } from './RowCtx'
import type { StreamTrace } from './streaming'

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

export default function StreamingTail({ traces, responseOnly, ctx }: {
  traces: StreamTrace[]
  responseOnly: boolean
  ctx: RowCtx
}) {
  if (responseOnly || traces.length === 0) return null
  const { colors, bubbleMax, humanId, isReason, onMention, onOpenDetail, displayName, thinkingExpanded, compactMode } = ctx
  return (
    <>
      {traces.map(({ traceId, thinking, text, workerId, lastTs, thinkingDone, textDone }) => {
        // Only the not-yet-finalized phase streams: once reason.thinking
        // lands, the terminal ThinkingBlock (rendered among the rows) takes
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
    </>
  )
}
