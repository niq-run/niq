import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'

interface WorkerBadgeMenuProps {
  x: number
  y: number
  workerId: string
  onDetail: () => void
  onFocus: () => void
  onClose: () => void
}

// Right-click menu on a talk sender's avatar/badge: 查看详情 / 只看该 worker.
// (Adding a worker to the filter is offered as a click affordance on the
// `@ recipient` label in the message instead, where it is actually meaningful.)
// Closes on outside click / Escape.
export default function WorkerBadgeMenu({ x, y, workerId, onDetail, onFocus, onClose }: WorkerBadgeMenuProps) {
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
        position: 'fixed', left: Math.min(x, (typeof window !== 'undefined' ? window.innerWidth : x) - 160),
        top: Math.min(y, (typeof window !== 'undefined' ? window.innerHeight : y) - 90),
        zIndex: 300, background: colors.bgLight, border: '1px solid ' + colors.border,
        borderRadius: 4, boxShadow: '0 4px 12px rgba(0,0,0,0.18)', padding: '3px 0',
      }}
    >
      <div className="btn-hover" style={item} onClick={() => { onDetail(); onClose() }}>{t('badge.menu.detail')}</div>
      <div className="btn-hover" style={item} onClick={() => { onFocus(); onClose() }}>{t('badge.menu.focus', { worker: workerId })}</div>
    </div>,
    document.body,
  )
}