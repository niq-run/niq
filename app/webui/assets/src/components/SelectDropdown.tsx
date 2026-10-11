import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'

interface SelectDropdownProps {
  value: string
  options: string[]
  placeholder: string
  onChange: (v: string) => void
  /** Optional custom rendering of each option (e.g. `#tag`). */
  render?: (o: string) => React.ReactNode
  /** Show a "clear" (placeholder) option at the top of the panel. Default true. */
  allowClear?: boolean
}

// SelectDropdown is a single-select dropdown styled like the sidebar picker's
// TagFilterDropdown: a trigger showing the current choice opens an absolute
// option panel; closes on outside click. Used for the worker-list filters
// (type / tag / online status) where a native <select> looked out of place.
export default function SelectDropdown({ value, options, placeholder, onChange, render, allowClear = true }: SelectDropdownProps) {
  const { colors } = useTheme()
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

  const active = value !== ''

  return (
    <div ref={wrapRef} style={{ position: 'relative', width: '100%' }}>
      {/* Trigger: shows the current choice; toggles the panel. */}
      <div
        onClick={(e) => { e.stopPropagation(); setOpen((v) => !v) }}
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
          {active ? (render ? render(value) : value) : placeholder}
        </span>
        <span style={{ fontSize: fontSizes.base, color: colors.textDimmed, flexShrink: 0 }}>{open ? '▴' : '▾'}</span>
      </div>

      {/* Panel: the placeholder (all) first, then each option. */}
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
            borderRadius: 4,
            boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
            zIndex: 30,
          }}
        >
          {allowClear && (
            <div
              onClick={() => { onChange(''); setOpen(false) }}
              style={{
                cursor: 'pointer', padding: '6px 9px', fontSize: fontSizes.base,
                color: !active ? colors.accent : colors.textDim,
                background: !active ? colors.bgChip : undefined,
              }}
            >
              {placeholder}
            </div>
          )}
          {options.map((o) => (
            <div
              key={o}
              onClick={() => { onChange(o); setOpen(false) }}
              style={{
                cursor: 'pointer', padding: '6px 9px', fontSize: fontSizes.base,
                color: o === value ? colors.accent : colors.text,
                background: o === value ? colors.bgChip : undefined,
              }}
            >
              {render ? render(o) : o}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}