// PinMark — the small "pinned to top" marker shown on a pinned worker row: a
// short horizontal bar with an upward arrow beneath it (the conventional "pin
// to top" glyph). Drawn with CSS rather than a unicode character so it renders
// identically across fonts and stays crisp at this size.
import type { Palette } from '../theme'

export default function PinMark({ colors, style }: { colors: Palette; style?: React.CSSProperties }) {
  return (
    <span
      title="pinned"
      aria-label="pinned"
      style={{
        marginLeft: 'auto',
        flexShrink: 0,
        display: 'inline-flex',
        flexDirection: 'column',
        alignItems: 'center',
        gap: 1,
        paddingLeft: 6,
        userSelect: 'none',
        ...style,
      }}
    >
      {/* top bar */}
      <span style={{ width: 7, height: 1.5, background: colors.accent, borderRadius: 1 }} />
      {/* upward arrow head */}
      <span
        style={{
          width: 0,
          height: 0,
          borderLeft: '3px solid transparent',
          borderRight: '3px solid transparent',
          borderBottom: '4.5px solid ' + colors.accent,
        }}
      />
    </span>
  )
}