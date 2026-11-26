// ContextUsagePanel: the popover behind a reason.response's trailing
// context-usage label. It owns only the popover chrome (placement, dismissal,
// the phone bottom sheet and the title bar); the report itself is
// ContextUsageReport, shared with the worker detail page's Context tab.
import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'
import { fontSizes, useTheme } from '../theme'
import { useI18n } from '../i18n'
import { useIsMobile } from '../hooks/useIsMobile'
import type { UsageMeta } from './talk-utils'
import ContextUsageReport from './ContextUsageReport'

// Anchor is the clicked label's viewport rect: the panel opens above it on
// desktop (the label sits at the end of a message) and is clamped to the
// viewport.
export interface UsageAnchor {
  left: number
  right: number
  top: number
  bottom: number
}

interface ContextUsagePanelProps {
  meta: UsageMeta
  anchor: UsageAnchor
  onClose: () => void
}

export default function ContextUsagePanel({ meta, anchor, onClose }: ContextUsagePanelProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const isMobile = useIsMobile()
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const onDown = (e: MouseEvent) => {
      const target = e.target as Element | null
      // The label that opened this panel toggles it itself; dismissing on its
      // mousedown would close the panel only for the click to reopen it.
      if (target?.closest?.('[data-usage-label]')) return
      if (ref.current && !ref.current.contains(target as Node)) onClose()
    }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [onClose])

  const surface: React.CSSProperties = {
    background: colors.bgLight,
    border: '1px solid ' + colors.border,
    boxShadow: '0 6px 24px rgba(0,0,0,0.28)',
    borderRadius: 8,
  }

  const body = (
    <div
      ref={ref}
      onMouseDown={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
      style={{ padding: isMobile ? '10px 14px 8px' : '14px 16px 12px', display: 'flex', flexDirection: 'column', gap: isMobile ? 8 : 10 }}
    >
      {/* Header: title + close */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
        <span style={{ fontSize: fontSizes.base, color: colors.text }}>{t('talk.usage.title')}</span>
        <button
          onClick={onClose}
          aria-label={t('talk.usage.close')}
          title={t('talk.usage.close')}
          style={{
            marginLeft: 'auto', background: 'transparent', border: 'none', cursor: 'pointer',
            color: colors.textDim, fontSize: fontSizes.lg, lineHeight: 1, padding: '2px 4px', borderRadius: 3,
          }}
        >
          {'\u00d7'}
        </button>
      </div>

      <ContextUsageReport
        contextWindow={meta.contextWindow}
        used={meta.total}
        breakdown={meta.breakdown}
        cache={{
          read: meta.cacheRead,
          creation: meta.cacheCreation,
          hitPercent: meta.cacheHitPercent,
          reported: meta.cacheReported,
        }}
        compact={isMobile}
        collapsibleGroups={isMobile}
      />
    </div>
  )

  if (isMobile) {
    return createPortal(
      <>
        <div onClick={onClose} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)', zIndex: 400 }} />
        <div
          style={{
            ...surface, position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: 401,
            borderRadius: '12px 12px 0 0', maxHeight: '72vh', overflowY: 'auto',
            paddingBottom: 'env(safe-area-inset-bottom)',
          }}
        >
          <div style={{ height: 4, width: 44, background: colors.border, borderRadius: 2, margin: '8px auto 0' }} />
          {body}
        </div>
      </>,
      document.body,
    )
  }

  // Desktop: anchored to the label, right edges aligned, on whichever side has
  // more room — and capped to that room, so a short window can scroll the panel
  // instead of pushing it off-screen.
  const vw = typeof window !== 'undefined' ? window.innerWidth : 0
  const vh = typeof window !== 'undefined' ? window.innerHeight : 0
  const width = 300
  const right = Math.max(8, Math.min(vw - anchor.right, vw - width - 8))
  const spaceAbove = Math.max(0, anchor.top - 8)
  const spaceBelow = Math.max(0, vh - anchor.bottom - 8)
  const openAbove = spaceAbove >= spaceBelow
  const maxHeight = Math.min(Math.max(openAbove ? spaceAbove : spaceBelow, 160), Math.round(vh * 0.7))
  return createPortal(
    <div
      style={{
        ...surface, position: 'fixed', width, right, zIndex: 300, maxHeight, overflowY: 'auto',
        ...(openAbove
          ? { bottom: vh - anchor.top + 8 }
          : { top: anchor.bottom + 8 }),
      }}
    >
      {body}
    </div>,
    document.body,
  )
}
