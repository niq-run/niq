import { useState, useRef, useEffect } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'

interface TagFilterDropdownProps {
  // Available tags to offer (checked against workers' tags in the caller).
  tags: string[]
  selected: string[]
  onToggle: (tag: string) => void
  onClear: () => void
}

// TagFilterDropdown is the expanded worker picker's multi-select tag filter: a
// full-width trigger showing the current selection state opens a dropdown of
// tag checkboxes. The trigger is sized to read as a peer of the picker's search
// input on the row above (same font, padding, radius), and the panel is
// height-capped and scrolls, so a long tag list never makes the dialog grow
// unboundedly tall.
export default function TagFilterDropdown({ tags, selected, onToggle, onClear }: TagFilterDropdownProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)

  // Close on a click outside the dropdown.
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) setOpen(false)
    }
    window.addEventListener('click', handler)
    return () => window.removeEventListener('click', handler)
  }, [open])

  const active = selected.length > 0

  return (
    <div ref={wrapRef} style={{ position: 'relative', width: '100%' }}>
      {/* Trigger: shows the selection state; toggles the panel. */}
      <div
        onClick={(e) => { e.stopPropagation(); setOpen(v => !v) }}
        title={active ? t('sidebar.tagFilter.tooltip.active') : t('sidebar.tagFilter.tooltip.idle')}
        style={{
          cursor: 'pointer',
          userSelect: 'none',
          display: 'flex',
          alignItems: 'center',
          gap: 6,
          width: '100%',
          boxSizing: 'border-box',
          justifyContent: 'space-between',
          fontSize: fontSizes.base,
          lineHeight: '1.4',
          padding: '7px 9px',
          borderRadius: 4,
          color: active ? colors.accent : colors.textDim,
          border: '1px solid ' + (active ? colors.accent : colors.border),
          background: active ? colors.bgChip : undefined,
        }}
      >
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
          {active
            ? `${t('sidebar.tagFilter.label')}: ${selected.length}`
            : t('sidebar.tagFilter.placeholder')}
        </span>
        <span style={{ fontSize: fontSizes.base, color: colors.textDimmed, flexShrink: 0 }}>{open ? '▴' : '▾'}</span>
      </div>

      {/* Panel: height-capped and scrolls when there are many tags. */}
      {open && (
        <div
          style={{
            position: 'absolute',
            left: 0,
            top: '100%',
            marginTop: 4,
            width: '100%',
            maxHeight: 220,
            overflowY: 'auto',
            background: colors.bgLight,
            border: '1px solid ' + colors.border,
            borderRadius: 6,
            boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
            zIndex: 120,
            padding: '4px 0',
          }}
        >
          {/* Clear-all row. */}
          <div
            onClick={(e) => { e.stopPropagation(); onClear(); }}
            className="btn-hover"
            style={{
              cursor: 'pointer',
              userSelect: 'none',
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              padding: '6px 12px',
              color: active ? colors.accent : colors.textDimmed,
              fontSize: fontSizes.sm,
              fontStyle: active ? 'normal' : 'italic',
              opacity: active ? 1 : 0.55,
              borderBottom: '1px solid ' + colors.border,
              marginBottom: 2,
            }}
          >
            <span
              style={{
                width: 13, height: 13, flexShrink: 0, marginRight: 2, display: 'inline-flex',
                alignItems: 'center', justifyContent: 'center', borderRadius: 3,
                border: '1px solid ' + (active ? colors.accent : colors.border),
                background: active ? colors.accent : 'transparent', color: '#fff', fontSize: 10, lineHeight: 1,
              }}
            >
              {active ? '✕' : ''}
            </span>
            {t('sidebar.tagFilter.clear')}
          </div>
          {tags.length === 0 && (
            <div style={{ padding: '8px 12px', color: colors.textDimmed, fontSize: fontSizes.sm }}>
              {t('sidebar.tagFilter.none')}
            </div>
          )}
          {tags.map(tag => {
            const on = selected.includes(tag)
            return (
              <div
                key={tag}
                onClick={(e) => { e.stopPropagation(); onToggle(tag) }}
                className="btn-hover"
                style={{
                  cursor: 'pointer',
                  userSelect: 'none',
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: '6px 12px',
                  color: on ? colors.accent : colors.textDim,
                  fontSize: fontSizes.sm,
                }}
              >
                <span
                  style={{
                    width: 13, height: 13, flexShrink: 0, display: 'inline-flex',
                    alignItems: 'center', justifyContent: 'center', borderRadius: 3,
                    border: '1px solid ' + (on ? colors.accent : colors.border),
                    background: on ? colors.accent : 'transparent', color: '#fff', fontSize: 10, lineHeight: 1,
                  }}
                >
                  {on ? '✓' : ''}
                </span>
                <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{'#' + tag}</span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
