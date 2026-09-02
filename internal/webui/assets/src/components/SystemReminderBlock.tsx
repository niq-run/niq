import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'

interface SystemReminderBlockProps {
  reminder: string
}

// Floor for the expanded slab: wide enough to read, small enough that most
// messages are already wider (so expanding leaves the bubble width alone).
const EXPANDED_MIN_WIDTH = 200

/**
 * Renders a `<system-reminder>` block (e.g. the lark bridge's Feishu context).
 * Collapsed it is a plain italic caption with no box; clicking wraps the
 * reminder in a slab spanning the bubble. Kept visually low-key so it does not
 * compete with the conversation.
 */
export default function SystemReminderBlock({ reminder }: SystemReminderBlockProps) {
  const { colors } = useTheme()
  const [open, setOpen] = useState(false)

  return (
    <div
      onClick={() => setOpen((v) => !v)}
      title={open ? 'click to collapse' : 'click to expand'}
      style={{
        marginTop: 6,
        marginBottom: 10,
        fontSize: fontSizes.xs,
        lineHeight: 1.6,
        color: colors.textDim,
        padding: open ? '4px 10px' : 0,
        border: open ? '1px solid ' + colors.detailBorder : 'none',
        background: open ? colors.bgLight : 'none',
        borderRadius: 5,
        cursor: 'pointer',
        userSelect: 'none',
        boxSizing: 'border-box',
        overflow: 'hidden',
        // inline-flex collapsed: shrink to the caption; block expanded: fill
        // the bubble. `contain: inline-size` keeps the reminder text out of the
        // bubble's shrink-to-fit width (collapsed it is a single line, so its
        // intrinsic width is the whole reminder), which is what previously
        // blew every bubble carrying a reminder up to the 70% cap.
        display: open ? 'block' : 'inline-flex',
        contain: open ? 'inline-size' : undefined,
        minWidth: open ? EXPANDED_MIN_WIDTH : undefined,
      }}
    >
      <span style={{ display: 'block', fontStyle: 'italic', color: colors.textDimmed, marginBottom: open ? 2 : 0 }}>
        system reminder
      </span>
      {open && <span style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>{reminder}</span>}
    </div>
  )
}
