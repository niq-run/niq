import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'

interface WorkerRowMenuProps {
  x: number
  y: number
  pinned: boolean
  slept: boolean
  onTogglePin: () => void
  onToggleSleep: () => void
  onClose: () => void
}

// A small context menu anchored at the right-click position, offering 置顶
// (pin) and 休眠 (sleep) for a worker row. Closes on outside click / Escape.
export default function WorkerRowMenu({ x, y, pinned, slept, onTogglePin, onToggleSleep, onClose }: WorkerRowMenuProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  const item: React.CSSProperties = {
    padding: '5px 12px', cursor: 'pointer', fontSize: fontSizes.sm,
    color: colors.text, whiteSpace: 'nowrap', userSelect: 'none',
  }

  return createPortal(
    <div
      ref={ref}
      onClick={(e) => e.stopPropagation()}
      style={{
        position: 'fixed', left: Math.min(x, (typeof window !== 'undefined' ? window.innerWidth : x) - 140),
        top: Math.min(y, (typeof window !== 'undefined' ? window.innerHeight : y) - 70),
        zIndex: 300, background: colors.bgLight, border: '1px solid ' + colors.border,
        borderRadius: 4, boxShadow: '0 4px 12px rgba(0,0,0,0.18)', padding: '3px 0',
      }}
    >
      <div className="btn-hover" style={item} onClick={() => { onTogglePin(); onClose() }}>
        {pinned ? t('pinned.unpin') : t('pinned.pin')}
      </div>
      <div className="btn-hover" style={{ ...item, color: colors.textDim }} onClick={() => { onToggleSleep(); onClose() }}>
        {slept ? t('pinned.wake') : t('pinned.sleep')}
      </div>
    </div>,
    document.body,
  )
}