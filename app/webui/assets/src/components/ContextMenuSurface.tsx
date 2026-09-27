import type { CSSProperties, ReactNode } from 'react'
import { useContextMenu, contextMenuElementStyle, type ContextMenuPosition } from '../hooks/useContextMenu'

interface ContextMenuSurfaceProps {
  // Trigger coordinates reported to onOpenMenu.
  onOpenMenu: (pos: ContextMenuPosition) => void
  style?: CSSProperties
  className?: string
  onClick?: () => void
  onMouseEnter?: () => void
  onMouseLeave?: () => void
  children: ReactNode
}

// A thin shell that grants its children a right-click (desktop) / long-press
// (mobile) context menu. The row content is passed through as `children`, and
// the gesture wiring (contextmenu + touch hold) plus the anti-text-selection
// styles are owned here so every call site behaves identically on phones.
export default function ContextMenuSurface({
  onOpenMenu, style, className, onClick, onMouseEnter, onMouseLeave, children,
}: ContextMenuSurfaceProps) {
  const handlers = useContextMenu((pos) => onOpenMenu(pos))
  return (
    <div
      {...handlers}
      className={className}
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
      style={style ? { ...style, ...contextMenuElementStyle } : { ...contextMenuElementStyle }}
    >
      {children}
    </div>
  )
}