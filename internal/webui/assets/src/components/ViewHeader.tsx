import type { ReactNode } from 'react'
import { useTheme, fontSizes, VIEW_HEADER_HEIGHT } from '../theme'

// Height aligned with the sidebar logo area (16px sidebar padding + 36px
// logo + 4px margin): every view's bottom border sits on the same line as
// the logo area's bottom edge.
// ViewHeader is the shared page header for the main-area views: fixed height,
// text vertically centered, bottom border on the logo line, unified title
// size (talk header size + 2). `right` holds the row's action pills.
export default function ViewHeader({ title, count, right }: { title: string; count?: number; right?: ReactNode }) {
  const { colors } = useTheme()
  return (
    <div style={{ height: VIEW_HEADER_HEIGHT, flexShrink: 0, display: 'flex', alignItems: 'center', gap: 10, padding: '0 24px', borderBottom: '1px solid ' + colors.border, fontSize: fontSizes.xl + 2, color: colors.text }}>
      <strong style={{ fontWeight: 600 }}>{title}</strong>
      {count !== undefined && <span style={{ color: colors.textMuted, fontSize: fontSizes.md }}>({count})</span>}
      <span style={{ marginLeft: 'auto', display: 'flex', alignItems: 'center', gap: 8, minWidth: 0 }}>{right}</span>
    </div>
  )
}
