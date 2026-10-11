import { fontSizes, type Palette } from '../theme'

interface DetailRowProps {
  label: string
  value: string
  colors: Palette
  /** Force-dimmer rendering even if the value is not a dash (e.g. "无/none"). */
  dim?: boolean
}

// DetailRow renders one "label → value" entry in an info block as its own row
// with a bottom divider (table-style). A placeholder value shown as a dash
// ('-' or '—') — i.e. an empty attribute — is rendered dimmer than a real value.
export default function DetailRow({ label, value, colors, dim }: DetailRowProps) {
  const trimmed = value.trim()
  const isDash = dim ?? (trimmed === '-' || trimmed === '\u2014')
  return (
    <div style={{ display: 'flex', gap: 16, alignItems: 'baseline', padding: '7px 0', borderBottom: '1px solid ' + colors.detailBorder }}>
      <span style={{ width: 120, flexShrink: 0, color: colors.detailLabel, fontSize: fontSizes.base }}>{label}</span>
      <span style={{ minWidth: 0, color: isDash ? colors.textDimmed : colors.detailValue, fontSize: fontSizes.base, wordBreak: 'break-all' }}>{value}</span>
    </div>
  )
}