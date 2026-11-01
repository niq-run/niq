import { useRef, useEffect } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useIsMobile } from '../hooks/useIsMobile'

export interface PickerOption {
  id: string
  label: string
  sublabel?: string
  hint?: string
  // Optional short description rendered as a dimmed second line.
  description?: string
}

interface PickerDropdownProps {
  header: string
  options: PickerOption[]
  selectedId?: string
  // selectedIds checks off several rows at once (the multi-worker mention
  // picker); selectedId stays the single-selection spelling.
  selectedIds?: string[]
  activeIndex?: number
  onSelect: (id: string) => void
  onActivate?: (index: number) => void
  width?: number
  // Optional live search: when both are provided the dropdown renders a search
  // box under the header. The caller owns the query state and passes the already
  // filtered options (see TalkInput); the dropdown only shows the box and
  // highlights the matching slice of each label. Omitted entirely for pickers
  // that don't need it (e.g. the input-mode selector).
  searchValue?: string
  onSearchValueChange?: (v: string) => void
  searchPlaceholder?: string
  // Optional "open the full dialog" affordance in the header: the dropdown is
  // a compact list, and a worker set that has outgrown it needs the room of a
  // centered dialog (WorkerPickerModal).
  onExpand?: () => void
  expandLabel?: string
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
  selectedIds,
  activeIndex,
  onSelect,
  onActivate,
  width = 240,
  searchValue,
  onSearchValueChange,
  searchPlaceholder,
  onExpand,
  expandLabel,
}: PickerDropdownProps) {
  const { colors } = useTheme()
  const isMobile = useIsMobile()
  const searchable = searchValue !== undefined && !!onSearchValueChange
  const searchRef = useRef<HTMLInputElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)

  // Keeps the keyboard-highlighted row visible while navigating with
  // arrow / Ctrl+n/p: when the active row moves outside the panel's scroll
  // viewport, bring it into view (nearest edge), so a long option list doesn't
  // "highlight off-screen" without scrolling.
  useEffect(() => {
    if (activeIndex == null || activeIndex < 0) return
    const el = panelRef.current?.querySelector<HTMLElement>(`[data-picker-idx="${activeIndex}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [activeIndex])

  // Split a label into [pre, match, post] around the first case-insensitive
  // occurrence of the query (if any), so the matching slice can be highlighted.
  const highlight = (text: string): React.ReactNode => {
    if (!searchable || !searchValue) return text
    const idx = text.toLowerCase().indexOf(searchValue.trim().toLowerCase())
    if (idx < 0) return text
    const end = idx + searchValue.trim().length
    return (
      <>
        {text.slice(0, idx)}
        <span style={{ background: colors.accentDim, color: colors.accent, borderRadius: 2, padding: '0 1px' }}>{text.slice(idx, end)}</span>
        {text.slice(end)}
      </>
    )
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
    background: colors.bg,
    border: '1px solid ' + colors.border,
    borderRadius: 6,
    maxHeight: '70vh',
    minHeight: 60,
    overflowY: 'auto',
    boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
    zIndex: 91,
  } : {
    width,
    maxWidth: width + 20,
    background: colors.bg,
    border: '1px solid ' + colors.border,
    borderRadius: 6,
    // A taller visible window than before so a long option list shows more at
    // once; a floor on the empty/very-short state keeps it from looking cramped.
    maxHeight: 320,
    minHeight: 108,
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
        ref={panelRef}
        style={panelStyle}
        // A callers' global window-click handler closes the picker; stop the
        // click from escaping the panel so interacting with the search box or
        // rows (which don't close it) doesn't dismiss the whole dropdown. The
        // mobile backdrop is a separate sibling, so tapping it still closes.
        onClick={(e) => e.stopPropagation()}
      >
      <div
        className="picker-header"
        style={{
          // A roomier top edge: the header's text sat almost on the panel's
          // border, which read as cramped on a panel that drops down over the
          // conversation.
          padding: isMobile ? '16px 12px 4px 12px' : '12px 10px 4px 10px',
          fontSize: isMobile ? fontSizes.base : fontSizes.sm,
          color: colors.textDimmed,
          display: 'flex', alignItems: 'center', gap: 8,
        }}
      >
        <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{header}</span>
        {onExpand && (
          /* A labelled button rather than an icon: the compact dropdown is the
             only place this entry point exists, and a glyph there went
             unnoticed. Sized like the input's other inline buttons. */
          <span
            role="button"
            className="btn-hover"
            onClick={onExpand}
            style={{
              cursor: 'pointer', userSelect: 'none', flexShrink: 0,
              color: colors.textDim, border: '1px solid ' + colors.border,
              borderRadius: 2, padding: '0 8px', lineHeight: '20px',
              fontSize: isMobile ? fontSizes.base : fontSizes.sm, whiteSpace: 'nowrap',
            }}
          >
            {expandLabel}
          </span>
        )}
      </div>
      {searchable && (
        <div style={{ padding: isMobile ? '6px 12px 8px 12px' : '4px 10px 6px 10px' }}>
          <input
            ref={searchRef}
            value={searchValue}
            onChange={(e) => onSearchValueChange!(e.target.value)}
            placeholder={searchPlaceholder ?? ''}
            spellCheck={false}
            style={{
              width: '100%', boxSizing: 'border-box',
              background: colors.bgLight, border: '1px solid ' + colors.border,
              borderRadius: 4, padding: isMobile ? '9px 10px' : '5px 8px', outline: 'none',
              color: colors.text, fontSize: isMobile ? fontSizes.base : fontSizes.sm,
            }}
          />
        </div>
      )}
      {options.map((opt, i) => {
        const selected = selectedIds ? selectedIds.includes(opt.id) : (selectedId !== undefined && selectedId === opt.id)
        const active = activeIndex === i
        return (
          <div
            key={opt.id}
            className="picker-row"
            data-picker-idx={i}
            title={opt.hint}
            onClick={() => onSelect(opt.id)}
            onMouseEnter={() => onActivate?.(i)}
            style={{
              padding: isMobile ? '12px' : '7px 12px',
              cursor: 'pointer',
              display: 'flex',
              alignItems: 'center',
              gap: isMobile ? 10 : 8,
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
              <span style={{ display: 'flex', alignItems: 'baseline', gap: 6, minWidth: 0, textAlign: 'left' }}>
                {/* The label hugs its sublabel instead of growing: an id should
                    sit right after the name it belongs to, not be flung to the
                    far edge of the row. */}
                <span style={{ flex: '0 1 auto', minWidth: 0, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                  {highlight(opt.label)}
                </span>
                {opt.sublabel && (
                  // Reads as a second identity, not as prose: dim, small, and
                  // tight against the label it qualifies.
                  <span style={{ flexShrink: 0, color: colors.textDimmed, fontSize: fontSizes.xs, whiteSpace: 'nowrap' }}>
                    {opt.sublabel}
                  </span>
                )}
              </span>
              {opt.description && (
                <span
                  style={{
                    fontSize: fontSizes.sm,
                    color: selected ? colors.accentDim : colors.textDim,
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
    </div>
    </>
  )
}