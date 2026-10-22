// PinMark — the small "pinned to top" marker shown on a pinned worker row: a
// short horizontal bar with an upward arrow beneath it (the conventional "pin
// to top" glyph). Drawn with CSS rather than a unicode character so it renders
// identically across fonts and stays crisp at this size.
import type { Palette } from '../theme'

export default function PinMark({ colors, color, style }: {
  colors: Palette
  // Colour of the mark. Callers pass the accent when the row is SELECTED and a
  // dim tone otherwise, so a pinned row reads as "pinned" without shouting
  // accent blue all the time.
  color?: string
  style?: React.CSSProperties
}) {
  const ink = color ?? colors.textDimmed
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
      <span style={{ width: 7, height: 1.5, background: ink, borderRadius: 1 }} />
      {/* upward arrow head */}
      <span
        style={{
          width: 0,
          height: 0,
          borderLeft: '3px solid transparent',
          borderRight: '3px solid transparent',
          borderBottom: '4.5px solid ' + ink,
        }}
      />
    </span>
  )
}