// Talk view row components: one component per event type, plus the shared
// WorkerBadge (speaker label) and the RowCtx carrying the colors/l10n/lookups/
// callbacks they read. TalkView computes the cheap per-row facts (alignment +
// whether to show the avatar) and hands each event to the matching row.
import { useState, memo, type ReactNode, type CSSProperties } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { useTheme, fontSizes, type Palette } from '../../theme'
import { useI18n, type StringKey } from '../../i18n'
import { makeMdComponents } from '../../components/MarkdownComponents'
import CollapsibleCode from '../../components/CollapsibleCode'
import ThinkingBlock from '../../components/ThinkingBlock'
import ResponseBlock from '../../components/ResponseBlock'
import SystemReminderBlock from '../../components/SystemReminderBlock'
import WorkerBadgeMenu from '../../components/WorkerBadgeMenu'
import {
  getInputText, isToolResult, toolContent, toolSummary, toolCallId,
  formatTime, findReferencedInput, splitSystemReminder, parseAttachments,
} from '../../components/talk-utils'
import type { EventPayload } from '../../types'

function WorkerBadge({ id, show, humanId, isReason, onMention, onOpenDetail, onAddFilter, onFocusWorker, displayName, small }: {
  id: string
  show: boolean
  humanId: string
  isReason: (id: string) => boolean
  onMention?: (id: string) => void
  onOpenDetail?: (id: string) => void
  onAddFilter?: (id: string) => void
  onFocusWorker?: (id: string) => void
  displayName: (id?: string) => string
  // small renders a compact trailing marker (font ~sm) instead of the big
  // speaker badge — used for the bottom-right run footer.
  small?: boolean
}) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [hover, setHover] = useState(false)
  // Right-click context menu position, or null.
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  if (!show) return null
  const isHuman = id === humanId
  const mentionable = !isHuman && isReason(id) && !!onMention
  const hasMenu = !!(onOpenDetail || onAddFilter || onFocusWorker)
  return (
    <span
      onClick={(e) => { e.stopPropagation(); if (mentionable) onMention?.(id) }}
      onContextMenu={(e) => {
        e.preventDefault(); e.stopPropagation();
        if (hasMenu) setMenu({ x: e.clientX, y: e.clientY })
      }}
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      className={mentionable ? 'badge-mention' : undefined}
      style={{
        cursor: mentionable ? 'pointer' : (hasMenu ? 'context-menu' : 'default'),
        fontSize: small ? fontSizes.sm : fontSizes.xxl,
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
      {menu && (
        <WorkerBadgeMenu
          x={menu.x}
          y={menu.y}
          workerId={id}
          onDetail={onOpenDetail ? () => onOpenDetail(id) : () => {}}
          onFocus={onFocusWorker ? () => onFocusWorker(id) : () => {}}
          onClose={() => setMenu(null)}
        />
      )}
    </span>
  )
}

// ── Row rendering ──
// Each talk event renders as one self-contained row component (below). They
// share a RowCtx carrying the colors/l10n/lookups/callbacks and the two
// per-row facts (alignment + whether to show the avatar), so the main render
// loop in TalkView reduces to "pick a row component and hand it the context"
// instead of an inline wall of JSX per event type.
interface RowCtx {
  dark: boolean
  colors: Palette
  t: (key: StringKey, vars?: Record<string, string | number>) => string
  isMobile: boolean
  compactMode: boolean
  thinkingExpanded: boolean
  bubbleMax: string
  tPad: string
  tFontSize: number
  itemSep: CSSProperties
  humanId: string
  displayName: (id?: string) => string
  isReason: (id: string) => boolean
  directionOf: (evt: EventPayload, alignRight?: boolean) => string
  expandedContent: Set<string>
  toggleExpanded: (key: string) => void
  // Tool/request cards are an accordion: at most one is open (openToolId), so
  // only one code body is rendered/highlighted at a time.
  openToolId: string | null
  toggleTool: (key: string) => void
  onMention?: (id: string) => void
  onOpenDetail?: (id: string) => void
  onAddFilter?: (id: string) => void
  onFocusWorker?: (id: string) => void
  onTraceClick: (traceId: string) => void
  onDecide?: (id: string, approved: boolean, note?: string) => void
  scrollToEvent: (evtId: string) => void
}

// RowBadge is the shared "avatar row for a speaker switch" wrapper used by
// most row components. Returns null when no badge is warranted.
function RowBadge({ ctx, workerId, showBadge, alignRight, extraPad = false }: {
  ctx: RowCtx
  workerId: string
  showBadge: boolean
  alignRight?: boolean
  extraPad?: boolean
}) {
  if (!showBadge) return null
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: extraPad ? 16 : undefined, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
      <WorkerBadge id={workerId} show={true} humanId={ctx.humanId} isReason={ctx.isReason} onMention={ctx.onMention} onOpenDetail={ctx.onOpenDetail} onAddFilter={ctx.onAddFilter} onFocusWorker={ctx.onFocusWorker} displayName={ctx.displayName} />
    </div>
  )
}

function InputRow({ evt, alignRight, showBadge, ctx }: { evt: EventPayload; alignRight: boolean; showBadge: boolean; ctx: RowCtx }) {
  const { colors, t } = ctx
  const parsed = parseAttachments(getInputText(evt))
  const { reminder, content } = splitSystemReminder(parsed.text)
  const rawMode = (evt.payload?.input_mode as string) || 'append'
  const modeKey = rawMode === 'interrupt' ? 'interrupt' : rawMode === 'schedule' ? 'schedule' : 'append'
  return (
    <div key={evt.id} data-evt-id={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} alignRight={alignRight} extraPad />
      <div
        style={{
          maxWidth: alignRight ? '70%' : ctx.bubbleMax,
          minWidth: 0,
          display: 'inline-block',
          textAlign: 'left',
          background: colors.bgLight,
          border: '1px solid ' + colors.border,
          padding: alignRight ? '10px 14px' : '6px 10px',
          fontSize: alignRight ? fontSizes.base : fontSizes.sm,
          lineHeight: 1.5,
          color: colors.text,
          boxSizing: 'border-box',
        }}
      >
        <div style={{ marginBottom: 4, display: 'flex', alignItems: 'baseline', gap: 6, justifyContent: alignRight ? 'flex-end' : 'flex-start', flexWrap: 'wrap' }}>
          {evt.target_worker_id ? (
            <>
              <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontWeight: 'bold' }}>{ctx.displayName(evt.worker_id)}</span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>@</span>
              <span
                onClick={(e) => { e.stopPropagation(); ctx.onAddFilter?.(evt.target_worker_id!) }}
                title={ctx.onAddFilter ? t('talk.addFilter.tooltip') : undefined}
                style={{ fontSize: fontSizes.sm, color: colors.accent, cursor: ctx.onAddFilter ? 'pointer' : undefined, textDecoration: ctx.onAddFilter ? 'underline dotted' : undefined, textUnderlineOffset: 3 }}
              >
                {ctx.displayName(evt.target_worker_id)}
              </span>
            </>
          ) : (
            <span style={{ fontSize: fontSizes.sm, color: colors.accent, fontWeight: 'bold' }}>{t('talk.broadcast')}</span>
          )}
          {modeKey && <span title={t(`mode.${modeKey}.hint`)} style={{ fontSize: fontSizes.xs, color: colors.textDimmed, userSelect: 'none' }}>{t(`mode.${modeKey}`)}</span>}
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
        </div>
        <div className="md-content">
          {reminder && <SystemReminderBlock reminder={reminder} />}
          {content ? <Markdown remarkPlugins={[remarkGfm]} components={makeMdComponents(ctx.dark, colors)}>{content}</Markdown> : null}
        </div>
        {parsed.attachments.length > 0 && (
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginTop: 6 }}>
            {parsed.attachments.map((a, i) => a.kind === 'image' ? (
              <img key={i} src={`data:${a.mime};base64,${a.data}`} alt={a.name || 'attachment'} style={{ maxWidth: '100%', maxHeight: 220, borderRadius: 4, border: '1px solid ' + colors.border, display: 'block' }} />
            ) : (
              <span key={i} title={a.path} style={{ fontSize: fontSizes.sm, color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 4, padding: '2px 8px' }}>{'\uD83D\uDCC4 ' + (a.name || a.path)}</span>
            ))}
          </div>
        )}
        {evt.trace_id && (
          <div style={{ marginTop: 6, textAlign: alignRight ? 'right' : 'left' }}>
            <span onClick={() => ctx.onTraceClick(evt.trace_id!)} style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }} title={t('talk.trace.tooltip')}>{t('talk.trace')}</span>
          </div>
        )}
      </div>
    </div>
  )
}

function AbortRow({ evt, alignRight, showBadge, ctx }: { evt: EventPayload; alignRight: boolean; showBadge: boolean; ctx: RowCtx }) {
  const { colors, t } = ctx
  return (
    <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 12, justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
        <WorkerBadge id={evt.worker_id} show={true} humanId={ctx.humanId} isReason={ctx.isReason} onMention={ctx.onMention} onOpenDetail={ctx.onOpenDetail} displayName={ctx.displayName} />
      </div>
      <div style={{ maxWidth: alignRight ? '70%' : ctx.bubbleMax, display: alignRight ? 'inline-block' : undefined, textAlign: 'left', background: colors.bgLight, border: '1px solid ' + colors.border, padding: '8px 12px', fontSize: fontSizes.sm, color: colors.textDim }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          {evt.target_worker_id && <span style={{ color: colors.textDimmed }}>to: {ctx.displayName(evt.target_worker_id)}</span>}
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
        </div>
        <div style={{ color: colors.text, fontSize: fontSizes.sm, marginTop: 4 }}>worker.abort</div>
      </div>
    </div>
  )
}

function TimerReminderRow({ evt, alignRight, showBadge, ctx }: { evt: EventPayload; alignRight: boolean; showBadge: boolean; ctx: RowCtx }) {
  const { colors, t } = ctx
  let reminderText = (evt.payload?.text as string) || (evt.payload?.purpose as string) || ''
  if (!reminderText && evt.payload?.result) {
    const result = evt.payload.result
    if (typeof result === 'string') {
      try { const p = JSON.parse(result); reminderText = p.purpose || p.text || '' } catch { reminderText = result }
    } else if (typeof result === 'object') {
      reminderText = (result as any).purpose || (result as any).text || ''
    }
  }
  return (
    <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} alignRight={alignRight} extraPad />
      <div style={{ maxWidth: alignRight ? '70%' : ctx.bubbleMax, display: alignRight ? 'inline-block' : undefined, textAlign: 'left', boxSizing: 'border-box', background: colors.bgLight, border: '1px solid ' + colors.border, padding: '10px 14px', fontSize: fontSizes.base, lineHeight: 1.5, color: colors.text }}>
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center', justifyContent: alignRight ? 'flex-end' : 'flex-start' }}>
          {evt.target_worker_id && <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>to: {ctx.displayName(evt.target_worker_id)}</span>}
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>{formatTime(evt.timestamp)}</span>
        </div>
        {reminderText && <div style={{ color: colors.text }}>{reminderText}</div>}
        {evt.trace_id && (
          <div style={{ marginTop: 6, textAlign: 'right' }}>
            <span onClick={() => ctx.onTraceClick(evt.trace_id!)} style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDimmed, textDecoration: 'underline', textDecorationStyle: 'dotted' }} title={t('talk.trace.tooltip')}>{t('talk.trace')}</span>
          </div>
        )}
      </div>
    </div>
  )
}

function CancelRow({ evt, ctx }: { evt: EventPayload; ctx: RowCtx }) {
  const { colors, t } = ctx
  const dir = ctx.directionOf(evt)
  return (
    <div key={evt.id} style={{ maxWidth: ctx.bubbleMax, marginBottom: ctx.compactMode ? 8 : 12 }}>
      <div style={{ border: '1px solid ' + colors.border, padding: ctx.tPad, fontSize: ctx.tFontSize, lineHeight: 1.5, color: colors.textDim }}>
        <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', rowGap: 4, columnGap: 8 }}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
            <span style={{ width: 8, height: 8, borderRadius: 4, background: colors.textDim, flexShrink: 0, opacity: 0.5 }} />
            <span>{t('talk.tool.cancelled')}</span>
          </span>
          {dir && <span style={{ ...ctx.itemSep, color: colors.textDimmed, whiteSpace: 'nowrap' }}>{dir}</span>}
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto', whiteSpace: 'nowrap' }}>{formatTime(evt.timestamp)}</span>
        </div>
      </div>
    </div>
  )
}

function InterruptedRow({ evt, ctx }: { evt: EventPayload; ctx: RowCtx }) {
  const { colors, t } = ctx
  const reason = (evt.payload?.reason as string) || ''
  const preserved = (evt.payload?.preserved_chars as number) || 0
  return (
    <div key={evt.id} style={{ maxWidth: ctx.bubbleMax, marginBottom: ctx.compactMode ? 8 : 12 }}>
      <div style={{ border: '1px solid ' + colors.border, padding: ctx.tPad, fontSize: ctx.tFontSize, lineHeight: 1.5, color: colors.textDim }}>
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
        {preserved > 0 && <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginTop: 4 }}>{t('talk.charsPreserved', { n: preserved })}</div>}
      </div>
    </div>
  )
}

function ThinkingRow({ evt, showBadge, ctx }: { evt: EventPayload; showBadge: boolean; ctx: RowCtx }) {
  return (
    <div key={evt.id + '-thinking-' + ctx.thinkingExpanded} style={{ maxWidth: ctx.bubbleMax }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} extraPad />
      <ThinkingBlock evt={evt} defaultExpanded={ctx.thinkingExpanded} compact={ctx.compactMode} />
    </div>
  )
}

function ResponseRowInner({ evt, showBadge, ctx, allEvents }: { evt: EventPayload; showBadge: boolean; ctx: RowCtx; allEvents: EventPayload[] }) {
  const ref = findReferencedInput(allEvents, evt)
  return (
    <div key={evt.id} data-evt-id={evt.id} style={{ maxWidth: ctx.bubbleMax }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} extraPad />
      <ResponseBlock evt={evt} quotedText={ref?.text} quotedWorker={ref?.workerId} quotedEvtId={ref?.evtId} onQuoteClick={ctx.scrollToEvent} />
    </div>
  )
}
// allEvents is excluded from the comparison on purpose: a live delta changes the
// events array reference but not any already-committed response's quoted input,
// so we'd otherwise re-render every response (and re-parse its markdown) on
// every event. The quote is recomputed only when the row itself re-renders.
const ResponseRow = memo(ResponseRowInner, (prev, next) =>
  prev.evt === next.evt && prev.showBadge === next.showBadge && prev.ctx === next.ctx,
)

function TimeoutRow({ evt, alignRight, showBadge, ctx }: { evt: EventPayload; alignRight: boolean; showBadge: boolean; ctx: RowCtx }) {
  const { colors, t } = ctx
  return (
    <div key={evt.id} style={{ marginBottom: 12, textAlign: alignRight ? 'right' : 'left' }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} alignRight={alignRight} extraPad />
      <div style={{ maxWidth: alignRight ? '70%' : ctx.bubbleMax, display: alignRight ? 'inline-block' : undefined, textAlign: 'left', boxSizing: 'border-box', background: colors.bgLight, border: '1px solid ' + colors.border, padding: '8px 12px', fontSize: fontSizes.sm, color: colors.textDim }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
          <span style={{ color: colors.text }}>{t('talk.timeout')}</span>
          {evt.target_worker_id && <span style={{ color: colors.textDimmed }}>to: {ctx.displayName(evt.target_worker_id)}</span>}
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
        </div>
      </div>
    </div>
  )
}

function ApprovalRowImpl({ evt, alignRight, showBadge, decision, ctx }: { evt: EventPayload; alignRight: boolean; showBadge: boolean; decision?: EventPayload; ctx: RowCtx }) {
  const { colors, t } = ctx
  const approved = decision?.payload?.approved === true
  const note = typeof decision?.payload?.note === 'string' ? decision.payload.note : ''
  const isExpanded = !ctx.expandedContent.has(evt.id)
  const statusColor = decision ? (approved ? colors.toolCompleted : colors.toolFailed) : colors.toolRequested
  return (
    <div key={evt.id} data-evt-id={evt.id} style={{ marginTop: 16, marginBottom: ctx.compactMode ? 8 : 12, textAlign: alignRight ? 'right' : 'left' }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} alignRight={alignRight} extraPad />
      <div className={!isExpanded ? 'block-card' : undefined} style={{ maxWidth: alignRight ? '70%' : ctx.bubbleMax, display: alignRight ? 'inline-block' : undefined, textAlign: 'left', boxSizing: 'border-box', border: '1px solid ' + colors.accent, padding: ctx.compactMode ? '4px 8px' : '6px 12px', fontSize: ctx.compactMode ? fontSizes.xs : fontSizes.base, lineHeight: 1.5, color: colors.textDim }}>
        <div onClick={() => ctx.toggleExpanded(evt.id)} style={{ cursor: 'pointer', userSelect: 'none', display: 'flex', alignItems: 'center', gap: 8 }}>
          <span style={{ width: 8, height: 8, borderRadius: 4, background: statusColor, flexShrink: 0, opacity: 0.5 }} />
          <span style={{ color: colors.text, fontWeight: 600 }}>{t('talk.approval.title')}</span>
          <span style={{ fontFamily: 'monospace', color: colors.text, fontSize: fontSizes.sm }}>{evt.worker_id}</span>
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
          <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs }}>{isExpanded ? '\u25BE' : '\u25B8'}</span>
        </div>
        {isExpanded && <div style={{ marginTop: 6, fontSize: fontSizes.sm, color: colors.text }}>{t(evt.payload?.action === 'mount.add' ? 'approval.reason.mount.add' : 'approval.reason.generic')}</div>}
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
          ctx.onDecide && (
            <div style={{ display: 'flex', gap: 8, marginTop: 10 }}>
              <span onClick={() => ctx.onDecide!(evt.id, true)} className="btn-hover" style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, color: colors.accent, borderRadius: 2, padding: '4px 12px', fontSize: fontSizes.md, userSelect: 'none' }}>{t('talk.approval.approve')}</span>
              <span onClick={() => ctx.onDecide!(evt.id, false)} className="btn-hover" style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, color: colors.textDim, borderRadius: 2, padding: '4px 12px', fontSize: fontSizes.md, userSelect: 'none' }}>{t('talk.approval.reject')}</span>
            </div>
          )
        )}
      </div>
    </div>
  )
}

function ToolRowImpl({ evt, alignRight, showBadge, resultEvt, partial, ctx }: { evt: EventPayload; alignRight: boolean; showBadge: boolean; resultEvt?: EventPayload; partial: string; ctx: RowCtx }) {
  const { colors, t, dark, isMobile } = ctx
  const isExpanded = ctx.openToolId === evt.id
  const content = toolContent(evt, isExpanded)
  const mergedResult = resultEvt ? toolContent(resultEvt, isExpanded) : ''
  const displayContent = mergedResult ? (content ? content + '\n\n—— result ——\n\n' + mergedResult : mergedResult) : content
  const contentLen = toolContent(evt, false).length + (resultEvt ? toolContent(resultEvt, false).length : 0)
  const summary = toolSummary(evt)
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
  const partialText = !resultEvt ? partial : ''
  const dir = ctx.directionOf(evt, alignRight)
  const segSep = <span style={{ color: colors.textDimmed, opacity: 0.6 }}>|</span>
  return (
    <div key={evt.id} style={{ marginBottom: ctx.compactMode ? 8 : 12, textAlign: alignRight ? 'right' : 'left' }}>
      <RowBadge ctx={ctx} workerId={evt.worker_id} showBadge={showBadge} alignRight={alignRight} extraPad />
      <div
        className={!isExpanded ? 'block-card' : undefined}
        style={{
          maxWidth: alignRight ? '70%' : ctx.bubbleMax,
          display: alignRight ? 'inline-block' : undefined,
          textAlign: 'left',
          boxSizing: 'border-box',
          border: '1px solid ' + (isExpanded ? colors.accent : colors.border),
          padding: ctx.tPad,
          fontSize: ctx.tFontSize,
          lineHeight: 1.5,
          color: colors.textDim,
          background: isExpanded ? (dark ? 'rgba(60,120,180,0.06)' : 'rgba(60,120,180,0.04)') : undefined,
        }}
      >
        <div onClick={() => ctx.toggleTool(evt.id)} style={{ cursor: 'pointer', userSelect: 'none' }}>
          {isMobile ? (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, whiteSpace: 'nowrap' }}>
                <span style={{ width: 8, height: 8, borderRadius: 4, background: statusColor, flexShrink: 0, opacity: 0.5 }} />
                <span style={{ color: colors.textDim, fontSize: ctx.tFontSize }}>{toolLabel} {summary}</span>
              </span>
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto', whiteSpace: 'nowrap' }}>{isExpanded ? '\u25BE' : '\u25B8'}</span>
            </div>
          ) : (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6 }}>
                <span style={{ width: 8, height: 8, borderRadius: 4, background: statusColor, flexShrink: 0, opacity: 0.5 }} />
                <span style={{ color: colors.textDim, fontSize: ctx.tFontSize }}>{toolLabel} {summary}</span>
              </span>
              {contentLen > 0 && (<>{segSep}<span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('thinking.chars', { n: contentLen })}</span></>)}
              {dir && (<>{segSep}<span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{dir}</span></>)}
              {isExpanded && contentLen > 0 && (<>{segSep}<span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('thinking.chars', { n: contentLen })}</span></>)}
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, marginLeft: 'auto' }}>{formatTime(evt.timestamp)}</span>
            </div>
          )}
        </div>
        {isMobile && isExpanded && (
          <div style={{ display: 'flex', alignItems: 'center', flexWrap: 'wrap', rowGap: 4, columnGap: 8, marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)'), fontSize: fontSizes.sm, color: colors.textDimmed }}>
            {dir && <span style={{ whiteSpace: 'nowrap' }}>{dir}</span>}
            <span style={{ marginLeft: 'auto', whiteSpace: 'nowrap' }}>{formatTime(evt.timestamp)}</span>
          </div>
        )}
        {isExpanded && displayContent && (
          <div style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid ' + (dark ? 'rgba(128,128,128,0.2)' : 'rgba(128,128,128,0.15)') }}>
            {resultEvt ? (
              <>
                <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.5px' }}>{t('talk.arguments')}</div>
                <CollapsibleCode code={content} language="json" />
                <div style={{ margin: '10px 0 6px', height: 1, background: dark ? 'rgba(128,128,128,0.25)' : 'rgba(128,128,128,0.18)' }} />
                <div style={{ fontSize: fontSizes.xs, color: resultEvt.type === 'request.failed' ? colors.toolFailed : resultEvt.type === 'request.rejected' ? colors.textDimmed : colors.toolCompleted, marginBottom: 4, textTransform: 'uppercase', letterSpacing: '0.5px' }}>{t('talk.result')}</div>
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

// Rows are memoized so a live SSE event only re-renders rows whose data
// actually changed (the streaming tail), not all of them — the measured
// bottleneck is per-event full-list script cost, so skipping unchanged rows
// (no markdown re-parse / no element rebuild for that subtree) is the win.
// Simple rows use default shallow compare on { evt, alignRight, showBadge,
// ctx }; the volatile-data rows compare their resolved result/decision/partial
// plus the (memoized, stable) ctx.
const MemoInputRow = memo(InputRow)
const MemoAbortRow = memo(AbortRow)
const MemoTimerReminderRow = memo(TimerReminderRow)
const MemoTimeoutRow = memo(TimeoutRow)
const MemoCancelRow = memo(CancelRow)
const MemoInterruptedRow = memo(InterruptedRow)
const MemoThinkingRow = memo(ThinkingRow)
const MemoApprovalRow = memo(ApprovalRowImpl, (p, n) =>
  p.evt === n.evt && p.alignRight === n.alignRight && p.showBadge === n.showBadge && p.decision === n.decision && p.ctx === n.ctx,
)
const MemoToolRow = memo(ToolRowImpl,
  (p, n) =>
    p.evt === n.evt && p.alignRight === n.alignRight && p.showBadge === n.showBadge &&
    p.resultEvt === n.resultEvt && p.partial === n.partial && p.ctx === n.ctx,
)

// Note: response rows are memoized above (ResponseRow) ignoring allEvents on
// purpose, so they don't re-render on every event.
export type { RowCtx }
export {
  WorkerBadge, RowBadge,
  MemoInputRow as InputRow, MemoAbortRow as AbortRow, MemoTimerReminderRow as TimerReminderRow,
  MemoTimeoutRow as TimeoutRow, MemoCancelRow as CancelRow, MemoInterruptedRow as InterruptedRow,
  MemoThinkingRow as ThinkingRow, ResponseRow,
  MemoApprovalRow as ApprovalRow, MemoToolRow as ToolRow,
}
