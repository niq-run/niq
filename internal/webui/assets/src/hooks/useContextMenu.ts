import { useCallback, useRef } from 'react'
import type { CSSProperties, MouseEvent as ReactMouseEvent, TouchEvent as ReactTouchEvent } from 'react'

export interface ContextMenuPosition {
  x: number
  y: number
}

// How long a finger must stay still before a context menu opens (ms).
const DEFAULT_HOLD_MS = 500
// A finger travelling farther than this cancels the long-press (the gesture is
// a scroll / drag, not a hold).
const MOVE_THRESHOLD = 12

export interface ContextMenuHandlers {
  onContextMenu: (e: ReactMouseEvent) => void
  onTouchStart: (e: ReactTouchEvent) => void
  onTouchMove: (e: ReactTouchEvent) => void
  onTouchEnd: () => void
  onTouchCancel: () => void
}

// Opens a context menu on either input modality: right-click/touchpad
// (contextmenu, immediate) or long-press on a touch screen. Long-press is
// driven by a timer because real phones do not fire `contextmenu` for a hold —
// they select text / raise the native callout instead. The element must also
// carry contextMenuElementStyle so user-select:none stops the browser from
// grabbing the hold before the timer can.
export function useContextMenu(
  open: (pos: ContextMenuPosition) => void,
  holdMs: number = DEFAULT_HOLD_MS,
): ContextMenuHandlers {
  const openRef = useRef(open)
  openRef.current = open
  const timer = useRef<number | null>(null)
  const start = useRef<{ x: number; y: number } | null>(null)

  const clear = useCallback(() => {
    if (timer.current != null) {
      window.clearTimeout(timer.current)
      timer.current = null
    }
    start.current = null
  }, [])

  const onContextMenu = useCallback((e: ReactMouseEvent) => {
    e.preventDefault()
    openRef.current({ x: e.clientX, y: e.clientY })
  }, [])

  const onTouchStart = useCallback((e: ReactTouchEvent) => {
    const t = e.touches[0]
    if (!t) return
    start.current = { x: t.clientX, y: t.clientY }
    timer.current = window.setTimeout(() => {
      timer.current = null
      openRef.current({ x: t.clientX, y: t.clientY })
    }, holdMs)
  }, [holdMs])

  const onTouchMove = useCallback((e: ReactTouchEvent) => {
    const t = e.touches[0]
    if (!t || !start.current) return
    const dx = t.clientX - start.current.x
    const dy = t.clientY - start.current.y
    if (Math.hypot(dx, dy) > MOVE_THRESHOLD) clear()
  }, [clear])

  const onTouchEnd = useCallback(() => { clear() }, [clear])
  const onTouchCancel = useCallback(() => { clear() }, [clear])

  return { onContextMenu, onTouchStart, onTouchMove, onTouchEnd, onTouchCancel }
}

// Spread into a context-menu-capable element's `style` to stop the browser's
// native long-press (text selection magnifier / iOS callout) from stealing the
// hold. Safari needs the prefixed variants.
export const contextMenuElementStyle: CSSProperties = {
  userSelect: 'none',
  WebkitUserSelect: 'none',
  WebkitTouchCallout: 'none',
}