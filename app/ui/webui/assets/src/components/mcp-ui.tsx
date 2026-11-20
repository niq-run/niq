import type { CSSProperties, ReactNode } from 'react'
import { fontSizes, type Palette } from '../theme'
import { useI18n } from '../i18n'
import { formStyles } from './Form'

// Small presentational pieces shared by the MCP roster and its detail panel.
// They live here so the two views agree on button/chip/section styling.

export const monoFont = 'ui-monospace, SFMono-Regular, Menlo, monospace'

export function errText(e: unknown): string {
  return (e as Error)?.message || String(e)
}

// Pill is a bordered action button; accent marks the primary action.
export function Pill({
  label,
  onClick,
  colors,
  accent,
  dim,
  grow,
  small,
}: {
  label: string
  onClick?: () => void
  colors: Palette
  accent?: boolean
  dim?: boolean
  grow?: boolean
  small?: boolean
}) {
  return (
    <span
      onClick={onClick}
      className="btn-hover"
      style={{
        flex: grow ? 1 : undefined,
        textAlign: grow ? 'center' : undefined,
        cursor: onClick ? 'pointer' : 'default',
        fontSize: small ? fontSizes.xs : fontSizes.sm,
        color: accent ? colors.accent : colors.textDim,
        border: '1px solid ' + (accent ? colors.accent : colors.border),
        borderRadius: 2,
        padding: small ? '2px 8px' : '3px 10px',
        userSelect: 'none',
        whiteSpace: 'nowrap',
      }}
    >
      {label}
    </span>
  )
}

// Action is a compact inline row action (tools, remove, save…).
export function Action({
  label,
  onClick,
  colors,
  active,
  danger,
}: {
  label: string
  onClick: () => void
  colors: Palette
  active?: boolean
  danger?: boolean
}) {
  const color = danger ? '#c33' : active ? colors.accent : colors.textDim
  const border = active ? colors.accent : danger ? '#c33' : colors.border
  return (
    <span
      onClick={onClick}
      className="btn-hover"
      style={{
        cursor: 'pointer',
        fontSize: fontSizes.xs,
        color,
        border: '1px solid ' + border,
        borderRadius: 3,
        padding: '0 8px',
        lineHeight: '18px',
        userSelect: 'none',
        whiteSpace: 'nowrap',
      }}
    >
      {label}
    </span>
  )
}

// Chip is a small labelled token: a value badge, or a selectable pill while
// editing (onClick present).
export function Chip({
  children,
  colors,
  active,
  onClick,
}: {
  children: ReactNode
  colors: Palette
  active?: boolean
  onClick?: () => void
}) {
  return (
    <span
      onClick={onClick}
      className={onClick ? 'btn-hover' : undefined}
      style={{
        display: 'inline-block',
        padding: '0 6px',
        borderRadius: 4,
        fontSize: fontSizes.xs,
        lineHeight: '18px',
        color: active ? colors.accent : colors.textDim,
        cursor: onClick ? 'pointer' : 'default',
        background: active ? colors.bgChip : colors.bgLight,
        border: '1px solid ' + (active ? colors.accent : colors.detailBorder),
        whiteSpace: 'nowrap',
        userSelect: 'none',
      }}
    >
      {children}
    </span>
  )
}

// Section is a titled card.
export function Section({
  title,
  right,
  colors,
  children,
}: {
  title: ReactNode
  right?: ReactNode
  colors: Palette
  children: ReactNode
}) {
  return (
    <div style={{ padding: '12px 14px', background: colors.detailBg, border: '1px solid ' + colors.detailBorder, borderRadius: 6 }}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
        <span style={{ color: colors.detailLabel, fontSize: fontSizes.sm, textTransform: 'uppercase', letterSpacing: '0.5px' }}>{title}</span>
        <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8 }}>{right}</span>
      </div>
      {children}
    </div>
  )
}

export function Row({ children, style }: { children: ReactNode; style?: CSSProperties }) {
  return <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', minWidth: 0, ...style }}>{children}</div>
}

export function Field({ label, hint, colors, children }: { label: string; hint?: string; colors: Palette; children: ReactNode }) {
  return (
    <div style={{ marginBottom: 8 }}>
      <label style={{ display: 'block', fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 3 }}>{label}</label>
      {children}
      {hint && <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 3, lineHeight: 1.5 }}>{hint}</div>}
    </div>
  )
}

// ProtocolSelect offers only the transport the worker speaks today; the local
// (stdio) option is present but disabled so the boundary is visible.
export function ProtocolSelect({ colors, value, onChange }: { colors: Palette; value: string; onChange: (v: string) => void }) {
  const { t } = useI18n()
  return (
    <select style={inputStyle(colors)} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="streamable-http">streamable-http</option>
      <option value="local-stdio" disabled>
        {t('mcp.server.protocolLocal')}
      </option>
    </select>
  )
}

export function labelStyle(colors: Palette): CSSProperties {
  return { fontSize: fontSizes.sm, color: colors.textDim, flexShrink: 0 }
}

export function hintStyle(colors: Palette): CSSProperties {
  return { fontSize: fontSizes.xs, color: colors.textDimmed, lineHeight: 1.5 }
}

export function inputStyle(colors: Palette): CSSProperties {
  return formStyles(colors).input
}

export function preStyle(colors: Palette): CSSProperties {
  return {
    margin: '4px 0 0',
    padding: 8,
    background: colors.bg,
    border: '1px solid ' + colors.detailBorder,
    borderRadius: 4,
    color: colors.textDim,
    fontSize: fontSizes.xs,
    fontFamily: monoFont,
    whiteSpace: 'pre-wrap',
    wordBreak: 'break-word',
    maxHeight: 260,
    overflow: 'auto',
  }
}

