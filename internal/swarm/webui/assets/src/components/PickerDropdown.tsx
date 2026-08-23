import { useTheme, fontSizes } from '../theme'

export interface PickerOption {
  id: string
  label: string
  sublabel?: string
  hint?: string
  // Optional short description rendered as a dimmed second line.
  description?: string
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
// highlighted and the selected row carries a check. Header sits with a little
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

  const rowBase: React.CSSProperties = {
    padding: '7px 12px',
    cursor: 'pointer',
    fontSize: fontSizes.base,
    display: 'flex',
    alignItems: 'center',
    gap: 8,
    fontFamily: 'monospace',
  }

  // Rows rely on the CSS :hover for their hover background. We must NOT paint
  // an inline `background` (not even `transparent`) on unhighlighted rows, or it
  // would override the class :hover rule.
  const highlightBg = colors.bgChip

  return (
    <div
      className="picker-dropdown"
      style={{
        width,
        maxWidth: width + 20,
        background: colors.bgLight,
        border: '1px solid ' + colors.border,
        borderRadius: 6,
        maxHeight: 220,
        overflowY: 'auto',
        boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
      }}
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
        return (
          <div
            key={opt.id}
            className="picker-row"
            title={opt.hint}
            onClick={() => onSelect(opt.id)}
            onMouseEnter={() => onActivate?.(i)}
            style={{
              padding: '7px 12px',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              fontSize: fontSizes.base,
              color: selected ? colors.accent : colors.textDim,
              background: active ? highlightBg : undefined,
            }}
          >
            {/* Dedicated left column for the check; spans the full row height, so\n                the value line and the description line share the same left x. */}
            <span
              style={{
                flexShrink: 0,
                width: 16,
                color: colors.accent,
                display: 'flex',
                alignItems: 'center',
              }}
            >
              {selected ? '✓' : ''}
            </span>
            <span style={{ flex: '1 1 auto', minWidth: 0, display: 'flex', flexDirection: 'column', gap: 2 }}>
              <span style={{ display: 'flex', alignItems: 'center', gap: 8, fontFamily: 'monospace', minWidth: 0 }}>
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
  )
}