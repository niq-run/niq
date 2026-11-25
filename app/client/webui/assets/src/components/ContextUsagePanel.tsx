// ContextUsagePanel: the detail popover behind a reason.response's trailing
// context-usage label. It breaks the round's prompt down into what the worker
// actually sent — the system prompt, the applied programs, the tool definitions
// and the transcript's message kinds — plus this round's generated output, and
// draws the same numbers as a stacked occupancy bar.
//
// The breakdown arrives on meta.usage.breakdown (see the reason worker's
// contextbreakdown.go); this panel only maps the stable section keys to
// localized labels, groups the fine-grained keys into the rows worth showing,
// and renders percentages against the model's context window.
import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { fontSizes, useTheme } from '../theme'
import { useI18n, type StringKey } from '../i18n'
import { useIsMobile } from '../hooks/useIsMobile'
import { formatCompact, formatPercent, type UsageMeta } from './talk-utils'

// Anchor is the clicked label's viewport rect: the panel opens above it on
// desktop (the label sits at the end of a message) and is clamped to the
// viewport.
export interface UsageAnchor {
  left: number
  right: number
  top: number
  bottom: number
}

interface ContextUsagePanelProps {
  meta: UsageMeta
  anchor: UsageAnchor
  onClose: () => void
}

// A group is one row of the legend: a set of breakdown keys summed together,
// with optional finer-grained child rows shown when the group is split.
interface UsageGroup {
  key: string
  label: StringKey
  color: string
  keys: string[]
  children?: { label: StringKey; keys: string[] }[]
}

// buildGroups defines the panel's rows. The worker emits fine-grained keys
// (per tool source, per message kind); the panel groups the ones that belong
// together so the legend stays scannable, while `children` preserves the detail
// when a group has more than one non-empty part.
function buildGroups(meta: UsageMeta): UsageGroup[] {
  const all: UsageGroup[] = [
    {
      key: 'system',
      label: 'talk.usage.system',
      color: 'system',
      keys: ['system'],
    },
    {
      key: 'program',
      label: 'talk.usage.program',
      color: 'program',
      keys: ['program.instructions', 'program.playbooks'],
      children: [
        { label: 'talk.usage.program.instructions', keys: ['program.instructions'] },
        { label: 'talk.usage.program.playbooks', keys: ['program.playbooks'] },
      ],
    },
    {
      key: 'tools',
      label: 'talk.usage.tools',
      color: 'tools',
      keys: ['tools.builtin', 'tools.program', 'tools.mcp', 'tools.workers', 'tools.other'],
      children: [
        { label: 'talk.usage.tools.builtin', keys: ['tools.builtin'] },
        { label: 'talk.usage.tools.program', keys: ['tools.program'] },
        { label: 'talk.usage.tools.mcp', keys: ['tools.mcp'] },
        { label: 'talk.usage.tools.workers', keys: ['tools.workers'] },
        { label: 'talk.usage.tools.other', keys: ['tools.other'] },
      ],
    },
    {
      key: 'messages',
      label: 'talk.usage.messages',
      color: 'messages',
      keys: ['messages.user', 'messages.assistant'],
      children: [
        { label: 'talk.usage.messages.user', keys: ['messages.user'] },
        { label: 'talk.usage.messages.assistant', keys: ['messages.assistant'] },
      ],
    },
    {
      key: 'toolCalls',
      label: 'talk.usage.toolCalls',
      color: 'toolCalls',
      keys: ['messages.tool_calls', 'messages.tool_results'],
      children: [
        { label: 'talk.usage.toolCalls.calls', keys: ['messages.tool_calls'] },
        { label: 'talk.usage.toolCalls.results', keys: ['messages.tool_results'] },
      ],
    },
    {
      key: 'thinking',
      label: 'talk.usage.thinking',
      color: 'thinking',
      keys: ['messages.thinking'],
    },
    {
      key: 'output',
      label: 'talk.usage.output',
      color: 'output',
      keys: ['output'],
    },
  ]
  return all.filter(g => g.keys.some(k => meta.breakdown.some(s => s.key === k && s.tokens > 0)))
}

// A compact number for the headline: one decimal for thousands/millions, so
// "85.6K / 300.0K" stays readable next to the percent.
function formatUsageNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return String(n)
}

// sumKeys sums the tokens the breakdown attributes to any of the given keys.
function sumKeys(breakdown: UsageMeta['breakdown'], keys: string[]): number {
  let n = 0
  for (const section of breakdown) {
    if (keys.includes(section.key)) n += section.tokens
  }
  return n
}

export default function ContextUsagePanel({ meta, anchor, onClose }: ContextUsagePanelProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const isMobile = useIsMobile()
  const ref = useRef<HTMLDivElement>(null)
  // Per-group detail expansion. On phone the sheet opens with only the
  // top-level rows (the full list does not fit); a tap reveals one group's
  // split. Desktop always shows the split, so this state is unused there.
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const target = e.target as Element | null
      // The label that opened this panel toggles it itself; dismissing on its
      // mousedown would close the panel only for the click to reopen it.
      if (target?.closest?.('[data-usage-label]')) return
      if (ref.current && !ref.current.contains(target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  const groups = useMemo(() => buildGroups(meta), [meta])
  const rows = useMemo(() => groups.map(g => {
    const tokens = sumKeys(meta.breakdown, g.keys)
    const children = (g.children || [])
      .map(c => ({ label: c.label, tokens: sumKeys(meta.breakdown, c.keys) }))
      .filter(c => c.tokens > 0)
    return { ...g, tokens, children }
  }).filter(g => g.tokens > 0), [groups, meta])

  const win = meta.contextWindow
  // Percent of the window; falls back to raw tokens when the window is unknown.
  const fraction = (tokens: number) => win > 0 ? formatPercent((tokens / win) * 100) + '%' : formatCompact(tokens)
  const barDenominator = meta.total > 0 ? meta.total : rows.reduce((sum, r) => sum + r.tokens, 0)
  const toggleGroup = (key: string) => setExpanded(prev => {
    const next = new Set(prev)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    return next
  })

  // Footer: the prompt cache is a property of the prompt, not a slice of it —
  // cache-read tokens overlap the breakdown sections, so they stay out of the
  // stacked bar and are reported as their own figure here.
  const footerParts = [
    meta.cacheReported ? t('talk.usage.cacheRate', { pct: formatPercent(meta.cacheHitPercent) }) : '',
    meta.cacheReported ? t('talk.usage.cacheRead', { n: formatCompact(meta.cacheRead) }) : '',
    meta.cacheCreation > 0 ? t('talk.usage.cacheWrite', { n: formatCompact(meta.cacheCreation) }) : '',
    win > 0 ? t('talk.usage.windowSize', { window: formatCompact(win) }) : '',
  ].filter(Boolean)

  const surface: React.CSSProperties = {
    background: colors.bgLight,
    border: '1px solid ' + colors.border,
    boxShadow: '0 6px 24px rgba(0,0,0,0.28)',
    borderRadius: 8,
  }

  const body = (
    <div
      ref={ref}
      onMouseDown={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
      style={{ padding: isMobile ? '10px 14px 8px' : '14px 16px 12px', display: 'flex', flexDirection: 'column', gap: isMobile ? 8 : 10 }}
    >
      {/* Header: title + close */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ fontSize: fontSizes.base, color: colors.text }}>{t('talk.usage.title')}</span>
        <button
          onClick={onClose}
          aria-label={t('talk.usage.close')}
          title={t('talk.usage.close')}
          style={{
            marginLeft: 'auto', background: 'transparent', border: 'none', cursor: 'pointer',
            color: colors.textDim, fontSize: fontSizes.lg, lineHeight: 1, padding: '2px 4px', borderRadius: 3,
          }}
        >
          {'\u00d7'}
        </button>
      </div>

      {/* Occupancy: the big percent plus used/window tokens */}
      {win > 0 && (
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
          <span style={{ fontSize: isMobile ? 22 : 26, fontWeight: 600, color: colors.text, lineHeight: 1.1 }}>
            {formatPercent(meta.contextPercent)}%
          </span>
          <span style={{ fontSize: fontSizes.sm, color: colors.textDim }}>
            {t('talk.usage.used')} {formatUsageNumber(meta.total)} / {formatUsageNumber(win)}
          </span>
        </div>
      )}
      {win <= 0 && (
        <div style={{ fontSize: fontSizes.sm, color: colors.textDim }}>
          {t('talk.usage.used')} {formatUsageNumber(meta.total)}
        </div>
      )}

      {/* Stacked composition bar: one segment per group, in legend order */}
      {win > 0 && barDenominator > 0 && (
        <div
          aria-hidden
          style={{
            display: 'flex', gap: 1, height: 8, borderRadius: 3, overflow: 'hidden',
            background: colors.borderLight,
          }}
        >
          {rows.map(r => (
            <div
              key={r.key}
              style={{
                width: `${(r.tokens / barDenominator) * 100}%`,
                background: colors.usage[r.color as keyof typeof colors.usage],
                minWidth: 2,
              }}
            />
          ))}
        </div>
      )}

      {/* Legend: a dot, a label, and the share of the window */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        {rows.map(r => {
          const color = colors.usage[r.color as keyof typeof colors.usage]
          const expandable = r.children.length > 1
          const open = expanded.has(r.key)
          // Phone: only the top-level row, unless the reader taps it open.
          const showChildren = expandable && (!isMobile || open)
          return (
            <div key={r.key} style={{ display: 'flex', flexDirection: 'column' }}>
              <div
                role={isMobile && expandable ? 'button' : undefined}
                onClick={isMobile && expandable ? () => toggleGroup(r.key) : undefined}
                title={isMobile && expandable ? t(open ? 'talk.processGroup.collapse' : 'talk.processGroup.expand') : undefined}
                style={{
                  display: 'flex', alignItems: 'center', gap: 8, padding: isMobile ? '2px 0' : '3px 0',
                  cursor: isMobile && expandable ? 'pointer' : undefined,
                }}
              >
                <span style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }} />
                <span style={{ fontSize: fontSizes.sm, color: colors.textMuted, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {t(r.label)}
                </span>
                <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
                  {isMobile && expandable && (
                    <span
                      aria-hidden
                      style={{
                        width: 0, height: 0,
                        borderTop: '3px solid transparent', borderBottom: '3px solid transparent',
                        borderLeft: '4px solid ' + colors.textDimmed,
                        transform: open ? 'rotate(90deg)' : 'none', transition: 'transform 0.12s',
                      }}
                    />
                  )}
                  <span style={{ fontSize: fontSizes.sm, color: colors.textDim, fontVariantNumeric: 'tabular-nums' }}>
                    {fraction(r.tokens)}
                  </span>
                </span>
              </div>
              {/* Detail rows: always on desktop; on phone only after a tap, and
                  only when the group actually splits. */}
              {showChildren && (
                <div style={{ display: 'flex', flexDirection: 'column', gap: 1, padding: '0 0 2px 16px' }}>
                  {r.children.map(c => (
                    <div key={c.label} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <span style={{ width: 5, height: 5, borderRadius: '50%', background: color, opacity: 0.6, flexShrink: 0 }} />
                      <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                        {t(c.label)}
                      </span>
                      <span style={{ marginLeft: 'auto', fontSize: fontSizes.xs, color: colors.textDimmed, flexShrink: 0, fontVariantNumeric: 'tabular-nums' }}>
                        {fraction(c.tokens)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>

      {footerParts.length > 0 && (
        <div style={{ borderTop: '1px solid ' + colors.border, paddingTop: 8, fontSize: fontSizes.xs, color: colors.textDim, textAlign: 'center' }}>
          {footerParts.join(' · ')}
        </div>
      )}
    </div>
  )

  if (isMobile) {
    return createPortal(
      <>
        <div onClick={onClose} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)', zIndex: 400 }} />
        <div
          style={{
            ...surface, position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: 401,
            borderRadius: '12px 12px 0 0', maxHeight: '72vh', overflowY: 'auto',
            paddingBottom: 'env(safe-area-inset-bottom)',
          }}
        >
          <div style={{ height: 4, width: 44, background: colors.border, borderRadius: 2, margin: '8px auto 0' }} />
          {body}
        </div>
      </>,
      document.body,
    )
  }

  // Desktop: anchored to the label, right edges aligned, on whichever side has
  // more room — and capped to that room, so a short window can scroll the panel
  // instead of pushing it off-screen.
  const vw = typeof window !== 'undefined' ? window.innerWidth : 0
  const vh = typeof window !== 'undefined' ? window.innerHeight : 0
  const width = 300
  const right = Math.max(8, Math.min(vw - anchor.right, vw - width - 8))
  const spaceAbove = Math.max(0, anchor.top - 8)
  const spaceBelow = Math.max(0, vh - anchor.bottom - 8)
  const openAbove = spaceAbove >= spaceBelow
  const maxHeight = Math.min(Math.max(openAbove ? spaceAbove : spaceBelow, 160), Math.round(vh * 0.7))
  return createPortal(
    <div
      style={{
        ...surface, position: 'fixed', width, right, zIndex: 300, maxHeight, overflowY: 'auto',
        ...(openAbove
          ? { bottom: vh - anchor.top + 8 }
          : { top: anchor.bottom + 8 }),
      }}
    >
      {body}
    </div>,
    document.body,
  )
}
