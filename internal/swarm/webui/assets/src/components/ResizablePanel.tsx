import { useRef, useState, type ReactNode, type MouseEvent as ReactMouseEvent } from 'react'
import { useTheme } from '../theme'

interface ResizablePanelProps {
  width: number
  minWidth: number
  maxWidth?: number
  onWidthChange: (w: number) => void
  children: ReactNode
}

/**
 * A right-hand panel with a drag handle on its left edge. The parent owns the
 * width state (so it can default to 40% / follow window resize); dragging here
 * reports new widths via onWidthChange.
 */
export default function ResizablePanel({ width, minWidth, maxWidth, onWidthChange, children }: ResizablePanelProps) {
  const { colors } = useTheme()
  const [hovering, setHovering] = useState(false)
  const widthRef = useRef(width)
  widthRef.current = width

  const startDrag = (e: ReactMouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = widthRef.current
    const maxW = maxWidth ?? Math.round(window.innerWidth * 0.7)
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
    <div style={{ position: 'absolute', top: 0, bottom: 0, right: 0, display: 'flex', zIndex: 5 }}>
      <div
        onMouseDown={startDrag}
        onMouseEnter={() => setHovering(true)}
        onMouseLeave={() => setHovering(false)}
        title="drag to resize"
        style={{ width: 12, flexShrink: 0, cursor: 'col-resize', position: 'relative', userSelect: 'none' }}
      >
        {/* Thin full-height line, flush with the panel's left edge */}
        <div style={{ position: 'absolute', top: 0, bottom: 0, right: 0, width: 1, background: colors.border }} />
        {/* Grip centered on the line, painted above it */}
        <div style={{ position: 'absolute', top: '50%', right: -2, width: 5, height: 30, marginTop: -15, borderRadius: 3, background: hovering ? colors.textDim : colors.textDimmed, zIndex: 1, transition: 'background 0.15s' }} />
      </div>
      {/* Panel content overlays the list beneath; a soft left shadow marks the edge */}
      <div style={{ width, minWidth, flexShrink: 0, display: 'flex', height: '100%', boxShadow: '-3px 0 8px rgba(0,0,0,0.15)' }}>
        {children}
      </div>
    </div>
  )
}
