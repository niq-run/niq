import { useRef, useState, type ReactNode, type MouseEvent as ReactMouseEvent } from 'react'
import { useTheme } from '../theme'
import { useI18n } from '../i18n'

interface ResizablePanelProps {
  // A CSS width — normally a percentage ("80%") so the drawer always fits its
  // container and follows window/container resizes. A number is treated as a
  // fixed pixel width (the value after a drag).
  width: number | string
  minWidth: number
  maxWidth?: number
  onWidthChange: (w: number) => void
  children: ReactNode
}

/**
 * A right-hand panel with a drag handle on its left edge. The parent owns the
 * width state; a percentage width is resolved against the panel's container (so
 * the drawer zooms with the window), and dragging snaps it to a fixed pixel
 * width, reporting the new value via onWidthChange.
 */
export default function ResizablePanel({ width, minWidth, maxWidth, onWidthChange, children }: ResizablePanelProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [hovering, setHovering] = useState(false)
  const panelRef = useRef<HTMLDivElement>(null)

  const startDrag = (e: ReactMouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    // Measure the current rendered width of the panel content. A percentage
    // width resolves to whatever the browser laid out; a number is as-is.
    const startW = panelRef.current?.offsetWidth ?? (typeof width === 'number' ? width : Math.round(window.innerWidth * 0.5))
    // The container to clamp against is the panel's (absolute) wrapper's
    // parent — the ProgramsView root — so the drawer never exceeds it.
    const containerW =
      panelRef.current?.parentElement?.parentElement?.offsetWidth ?? panelRef.current?.parentElement?.clientWidth ?? window.innerWidth
    const maxW = maxWidth ?? Math.max(minWidth, Math.round(containerW * 0.98))
    const onMove = (ev: MouseEvent) => {
      const next = startW + (startX - ev.clientX)
      onWidthChange(Math.min(Math.max(next, minWidth), maxW))
    }
    const onUp = () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
    }
    document.body.style.cursor = 'col-resize'
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  return (
    <div style={{ position: 'absolute', top: 0, bottom: 0, right: 0, display: 'flex', zIndex: 5, width, minWidth }}>
      <div
        onMouseDown={startDrag}
        onMouseEnter={() => setHovering(true)}
        onMouseLeave={() => setHovering(false)}
        title={t('panel.resize.tooltip')}
        style={{ width: 12, flexShrink: 0, cursor: 'col-resize', position: 'relative', userSelect: 'none' }}
      >
        {/* Thin full-height line, flush with the panel's left edge */}
        <div style={{ position: 'absolute', top: 0, bottom: 0, right: 0, width: 1, background: colors.border }} />
        {/* Grip centered on the line, painted above it */}
        <div style={{ position: 'absolute', top: '50%', right: -2, width: 5, height: 30, marginTop: -15, borderRadius: 3, background: hovering ? colors.textDim : colors.textDimmed, zIndex: 1, transition: 'background 0.15s' }} />
      </div>
      {/* Panel content overlays the list beneath; a soft left shadow marks the edge */}
      <div ref={panelRef} style={{ flex: 1, minWidth: 0, display: 'flex', height: '100%', boxShadow: '-3px 0 8px rgba(0,0,0,0.15)' }}>
        {children}
      </div>
    </div>
  )
}