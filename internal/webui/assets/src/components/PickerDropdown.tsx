import { useTheme, fontSizes } from '../theme'
import { useIsMobile } from '../hooks/useIsMobile'

export interface PickerOption {
  id: string
  label: string
  sublabel?: string
  hint?: string
  // Optional short description rendered as a dimmed second line.
  description?: string
  // isGroup marks a non-selectable group header (e.g. a tag group in the
  // worker target picker). Group headers are inert rows for structure only.
  isGroup?: boolean
  // indent is the left padding depth for nesting under a group, mapping
  // slash-path tag depth to visual indentation.
  indent?: number
}

export interface PickerFooter {
  label: string
  checked: boolean
  onChoose: () => void
}

interface PickerDropdownProps {
  header: string
  options: PickerOption[]
  selectedId?: string
  activeIndex?: number
  onSelect: (id: string) => void
  onActivate?: (index: number) => void
  footer?: PickerFooter
  width?: number
}

// Reusable option dropdown (used for the talk target picker, the @mention
// picker, and the input-mode selector). Rows never wrap; the active row is
// highlighted and the selected row carries a check at the far right (in its own
// reserved column, so it never squeezes the text). Header sits with a little
// breathing room under the top border, and each row has a hover style.
export default function PickerDropdown({
  header,
  options,
  selectedId,
  activeIndex,
  onSelect,
  onActivate,
  footer,
  width = 240,
}: PickerDropdownProps) {
  const { colors } = useTheme()
  const isMobile = useIsMobile()

  const rowBase: React.CSSProperties = {
    padding: '7px 12px',
    cursor: 'pointer',
    fontSize: fontSizes.base,
    display: 'flex',
    alignItems: 'center',
    gap: 8,
  }

  // Rows rely on the CSS :hover for their hover background. We must NOT paint
  // an inline `background` (not even `transparent`) on unhighlighted rows, or it
  // would override the class :hover rule.
  //
  // The keyboard-active row (ctrl+n/p / arrow keys) uses the SAME subtle
  // overlay as the CSS :hover rule (.picker-row:hover) so that navigating looks
  // exactly like hovering — the two states are visually identical. Using an
  // opaque chip color here would render the active row differently from hover
  // (darker than the panel in dark mode, ~invisible in light mode), which is
  // what made it look wrong until the row was hovered.
  const highlightBg = 'rgba(128,128,128,0.15)'

  // Mobile: the picker becomes a centered dialog (it cannot fit an anchored
  // dropdown beside the trigger on a phone). Desktop keeps the anchored panel
  // that the caller positions.
  const panelWidth = isMobile
    ? Math.min(width, (typeof window !== 'undefined' ? window.innerWidth : width) - 32)
    : width
  const panelStyle: React.CSSProperties = isMobile ? {
    position: 'fixed',
    top: '50%',
    left: '50%',
    transform: 'translate(-50%, -50%)',
    width: panelWidth,
    maxWidth: panelWidth + 20,
    background: colors.bgLight,
    border: '1px solid ' + colors.border,
    borderRadius: 6,
    maxHeight: '70vh',
    overflowY: 'auto',
    boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
    zIndex: 91,
  } : {
    width,
    maxWidth: width + 20,
    background: colors.bgLight,
    border: '1px solid ' + colors.border,
    borderRadius: 6,
    maxHeight: 220,
    overflowY: 'auto',
    boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
  }

  return (
    <>
      {/* Backdrop: tapping it closes the dialog (the callers close via their
          global window click handler, which this click bubbles to). */}
      {isMobile && <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', zIndex: 90 }} />}
      <div
        className="picker-dropdown"
        style={panelStyle}
      >
      <div
        className="picker-header"
        style={{ padding: '8px 10px 3px 10px', fontSize: fontSizes.xs, color: colors.textDimmed }}
      >
        {header}
      </div>
      {options.map((opt, i) => {
        const selected = selectedId !== undefined && selectedId === opt.id
        const active = activeIndex === i
        // Group headers are inert structural rows: no selection, no hover
        // affordance, no check column.
        if (opt.isGroup) {
          return (
            <div
              key={opt.id}
              className="picker-group"
              style={{
                padding: '8px 12px 2px 12px',
                fontSize: fontSizes.xs,
                color: colors.textDimmed,
                fontWeight: 600,
                letterSpacing: '0.02em',
                textTransform: 'uppercase',
                whiteSpace: 'nowrap',
                overflow: 'hidden',
                textOverflow: 'ellipsis',
                userSelect: 'none',
              }}
            >
              {opt.label}
            </div>
          )
        }
        const padLeft = 12 + (opt.indent ?? 0) * 12
        return (
          <div
            key={opt.id}
            className="picker-row"
            title={opt.hint}
            onClick={() => onSelect(opt.id)}
            onMouseEnter={() => onActivate?.(i)}
            style={{
              padding: `7px 12px 7px ${padLeft}px`,
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              fontSize: fontSizes.base,
              color: selected ? colors.accent : colors.textDim,
              background: active ? highlightBg : undefined,
            }}
          >
            {/* Text block: left-aligned, flexes to fill. */}
            <span
              style={{
                flex: '1 1 auto',
                minWidth: 0,
                display: 'flex',
                flexDirection: 'column',
                gap: 2,
                textAlign: 'left',
              }}
            >
              <span style={{ display: 'flex', alignItems: 'center', gap: 8, minWidth: 0, textAlign: 'left' }}>
                <span style={{ flex: '1 1 auto', minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {opt.label}
                </span>
                {opt.sublabel && (
                  <span style={{ flexShrink: 0, color: colors.textDimmed, fontStyle: 'italic', fontSize: fontSizes.sm }}>
                    {opt.sublabel}
                  </span>
                )}
              </span>
              {opt.description && (
                <span
                  style={{
                    fontSize: fontSizes.xs,
                    color: selected ? colors.accentDim : colors.textDimmed,
                    lineHeight: 1.4,
                    whiteSpace: 'nowrap',
                    overflow: 'hidden',
                    textOverflow: 'ellipsis',
                  }}
                >
                  {opt.description}
                </span>
              )}
            </span>
            {/* Reserved right column for the selection check — the check never
                squeezes the text, and the text always starts flush left. */}
            <span
              style={{
                flexShrink: 0,
                width: 16,
                textAlign: 'right',
                color: colors.accent,
                lineHeight: 1,
              }}
            >
              {selected ? '✓' : ''}
            </span>
          </div>
        )
      })}
      {footer && (
        <div
          className="picker-row"
          onClick={footer.onChoose}
          style={{
            ...rowBase,
            color: colors.textDimmed,
            borderTop: '1px solid ' + colors.border,
            fontStyle: 'italic',
            whiteSpace: 'nowrap',
          }}
        >
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{footer.label}</span>
          {footer.checked && <span style={{ marginLeft: 'auto', color: colors.accent }}>✓</span>}
        </div>
      )}
    </div>
    </>
  )
}