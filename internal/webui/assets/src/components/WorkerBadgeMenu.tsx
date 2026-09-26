import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { useIsMobile } from '../hooks/useIsMobile'

interface WorkerBadgeMenuProps {
  x: number
  y: number
  workerId: string
  onDetail: () => void
  onFocus: () => void
  onClose: () => void
}

// Context menu on a talk sender's avatar/badge: 查看详情 / 只看该 worker.
// (Adding a worker to the filter is offered as a click affordance on the
// `@ recipient` label in the message instead, where it is actually meaningful.)
//
// Responsive: on phone (long-press) it renders a full-width bottom sheet with
// large tap targets and a dimmed backdrop; on desktop it is the small menu
// anchored at the cursor. Closes on outside click / backdrop / Escape.
export default function WorkerBadgeMenu({ x, y, workerId, onDetail, onFocus, onClose }: WorkerBadgeMenuProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const isMobile = useIsMobile()
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
    padding: '14px 20px', cursor: 'pointer', fontSize: isMobile ? fontSizes.base : fontSizes.sm,
    color: colors.text, whiteSpace: 'nowrap', userSelect: 'none', display: 'flex', alignItems: 'center',
  }

  if (isMobile) {
    // Phone: a bottom sheet — full-width panel at the bottom, dimmed backdrop.
    return createPortal(
      <>
        <div onClick={onClose} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)', zIndex: 400 }} />
        <div
          ref={ref}
          onClick={(e) => e.stopPropagation()}
          style={{
            position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: 401,
            background: colors.bgLight, borderTop: '1px solid ' + colors.border,
            borderRadius: '12px 12px 0 0', boxShadow: '0 -4px 20px rgba(0,0,0,0.28)',
            paddingBottom: 'env(safe-area-inset-bottom)',
          }}
        >
          <div style={{ height: 4, width: 44, background: colors.border, borderRadius: 2, margin: '10px auto 6px' }} />
          <div style={{ padding: '4px 8px 8px 20px', color: colors.textDim, fontSize: fontSizes.sm }}>{workerId}</div>
          <div className="btn-hover" style={item} onClick={() => { onDetail(); onClose() }}>{t('badge.menu.detail')}</div>
          <div className="btn-hover" style={{ ...item, borderTop: '1px solid ' + colors.border }} onClick={() => { onFocus(); onClose() }}>
            {t('badge.menu.focus', { worker: workerId })}
          </div>
        </div>
      </>,
      document.body,
    )
  }

  // Desktop: small menu anchored at the cursor.
  const itemDesktop: React.CSSProperties = { ...item, padding: '5px 12px', fontSize: fontSizes.sm }
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
      <div className="btn-hover" style={itemDesktop} onClick={() => { onDetail(); onClose() }}>{t('badge.menu.detail')}</div>
      <div className="btn-hover" style={itemDesktop} onClick={() => { onFocus(); onClose() }}>{t('badge.menu.focus', { worker: workerId })}</div>
    </div>,
    document.body,
  )
}