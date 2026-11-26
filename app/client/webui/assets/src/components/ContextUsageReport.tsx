// ContextUsageReport renders a round's context accounting: the occupancy
// headline, a stacked bar of the prompt's parts, a legend with each part's
// share, and the cache accounting underneath. It is shared by the trailing
// label's popover (ContextUsagePanel) and the worker detail page's Context tab,
// so the same numbers always read the same way.
//
// The data is a list of `{key, tokens}` sections whose keys are the worker's
// stable wire identifiers (see core/impl/reason/contextbreakdown.go). This file
// owns the mapping from those keys to localized labels and to the grouped rows
// the legend shows; the keys themselves never reach the UI layer.
import { useMemo, useState } from 'react'
import { fontSizes, useTheme } from '../theme'
import { useI18n, type StringKey } from '../i18n'
import { formatCompact, formatPercent, type UsageSection } from './talk-utils'

// UsageCacheInfo is the prompt-cache half of a report. `reported` separates
// "the provider reports cache accounting and it was 0" from "the provider said
// nothing" — only the former is worth a 0% row.
export interface UsageCacheInfo {
  read: number
  creation: number
  hitPercent: number
  reported: boolean
}

export interface ContextUsageReportProps {
  // contextWindow is the active model's window (0 = unknown → percentages fall
  // back to raw token counts).
  contextWindow: number
  // used is the round's prompt + generated tokens.
  used: number
  breakdown: UsageSection[]
  cache?: UsageCacheInfo
  // compact tightens spacing and type for a phone-width popover.
  compact?: boolean
  // collapsibleGroups starts each group's detail rows collapsed and expands
  // them on tap. The popover uses it on phones; a detail page has room and
  // always shows the split.
  collapsibleGroups?: boolean
}

// UsageGroup is one legend row: a set of breakdown keys summed together, with
// optional finer-grained children shown when the group is split.
interface UsageGroup {
  key: string
  label: StringKey
  color: string
  keys: string[]
  children?: { label: StringKey; keys: string[] }[]
}

// buildGroups defines the rows. The worker emits fine-grained keys (per tool
// source, per message kind); grouping the ones that belong together keeps the
// legend scannable while `children` preserves the detail.
function buildGroups(breakdown: UsageSection[]): UsageGroup[] {
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
  return all.filter(g => g.keys.some(k => breakdown.some(s => s.key === k && s.tokens > 0)))
}

// sumKeys sums the tokens the breakdown attributes to any of the given keys.
function sumKeys(breakdown: UsageSection[], keys: string[]): number {
  let n = 0
  for (const section of breakdown) {
    if (keys.includes(section.key)) n += section.tokens
  }
  return n
}

// formatUsageNumber is the headline's number format: one decimal for
// thousands/millions, so "85.6K / 300.0K" stays readable next to the percent.
export function formatUsageNumber(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1000) return (n / 1000).toFixed(1) + 'K'
  return String(n)
}

export default function ContextUsageReport({
  contextWindow, used, breakdown, cache, compact, collapsibleGroups,
}: ContextUsageReportProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [expanded, setExpanded] = useState<Set<string>>(new Set())

  const groups = useMemo(() => buildGroups(breakdown), [breakdown])
  const rows = useMemo(() => groups.map(g => {
    const tokens = sumKeys(breakdown, g.keys)
    const children = (g.children || [])
      .map(c => ({ label: c.label, tokens: sumKeys(breakdown, c.keys) }))
      .filter(c => c.tokens > 0)
    return { ...g, tokens, children }
  }).filter(g => g.tokens > 0), [groups, breakdown])

  const win = contextWindow
  const fraction = (tokens: number) => win > 0 ? formatPercent((tokens / win) * 100) + '%' : formatCompact(tokens)
  const barDenominator = used > 0 ? used : rows.reduce((sum, r) => sum + r.tokens, 0)
  const pct = win > 0 ? formatPercent((used / win) * 100) : '0'

  const toggleGroup = (key: string) => setExpanded(prev => {
    const next = new Set(prev)
    if (next.has(key)) next.delete(key)
    else next.add(key)
    return next
  })

  // The cache is a property of the prompt, not a slice of it: cache-read tokens
  // overlap the sections above, so they stay out of the bar and live here.
  const footerParts = [
    cache?.reported ? t('talk.usage.cacheRate', { pct: formatPercent(cache.hitPercent) }) : '',
    cache && cache.creation > 0 ? t('talk.usage.cacheWrite', { n: formatCompact(cache.creation) }) : '',
    win > 0 ? t('talk.usage.windowSize', { window: formatCompact(win) }) : '',
  ].filter(Boolean)

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: compact ? 8 : 10 }}>
      {/* Occupancy: the big percent plus used/window tokens */}
      <div style={{ display: 'flex', alignItems: 'baseline', gap: 8, flexWrap: 'wrap' }}>
        <span style={{ fontSize: compact ? 22 : 26, fontWeight: 600, color: colors.text, lineHeight: 1.1 }}>
          {win > 0 ? pct + '%' : '\u2014'}
        </span>
        <span style={{ fontSize: fontSizes.sm, color: colors.textDim }}>
          {t('talk.usage.used')} {formatUsageNumber(used)}{win > 0 ? ' / ' + formatUsageNumber(win) : ''}
        </span>
      </div>

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
          const showChildren = expandable && (!collapsibleGroups || open)
          return (
            <div key={r.key} style={{ display: 'flex', flexDirection: 'column' }}>
              <div
                role={collapsibleGroups && expandable ? 'button' : undefined}
                onClick={collapsibleGroups && expandable ? () => toggleGroup(r.key) : undefined}
                title={collapsibleGroups && expandable ? t(open ? 'talk.processGroup.collapse' : 'talk.processGroup.expand') : undefined}
                style={{
                  display: 'flex', alignItems: 'center', gap: 8, padding: compact ? '2px 0' : '3px 0',
                  cursor: collapsibleGroups && expandable ? 'pointer' : undefined,
                }}
              >
                <span style={{ width: 8, height: 8, borderRadius: '50%', background: color, flexShrink: 0 }} />
                <span style={{ fontSize: fontSizes.sm, color: colors.textMuted, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                  {t(r.label)}
                </span>
                <span style={{ marginLeft: 'auto', display: 'inline-flex', alignItems: 'center', gap: 6, flexShrink: 0 }}>
                  {collapsibleGroups && expandable && (
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
              {/* Detail rows: always here, unless the caller collapses them. */}
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
}
