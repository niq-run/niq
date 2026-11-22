import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { fetchWorkers } from '../services/api'
import type { WorkerInfo } from '../types'
import { PublishAllowRows, SubscribeAllowRows, type PublishRow, type SubscribeRow } from './AllowEditors'
import { buttonStyles } from './Form'

interface PermissionPanelProps {
  publish: PublishRow[]
  subscribe: SubscribeRow[]
  onPublishChange: (rows: PublishRow[]) => void
  onSubscribeChange: (rows: SubscribeRow[]) => void
  // Live worker list, used by the "pick a worker's published events" helper in
  // the subscription editor. When not supplied (the template builder has no
  // worker list of its own) the panel fetches it when the dialog opens.
  workers?: WorkerInfo[]
  defaultOpen?: boolean
}

// PermissionPanel is the create form's "订阅发布管理" module: one 权限管理 button
// that opens the publish/subscribe editors in a dialog of their own, instead of
// growing the parent form. Rows are draft state owned by the caller, so the
// panel works both for a worker being created and for a template's worker entry
// being built.
export default function PermissionPanel({
  publish,
  subscribe,
  onPublishChange,
  onSubscribeChange,
  workers,
  defaultOpen = false,
}: PermissionPanelProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [open, setOpen] = useState(defaultOpen)
  const [fetched, setFetched] = useState<WorkerInfo[]>([])
  const allWorkers = workers ?? fetched
  const btn = buttonStyles(colors)

  // The editors open over the create dialog, which is itself a transformed
  // fixed panel: a fixed child would be clipped by it, so the dialog renders
  // through a portal to the body.
  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  // Fetch the worker list lazily: only once the dialog is opened AND the caller
  // did not provide one. A cancelled flag keeps a slow reply from landing after
  // the dialog closed or the caller switched to its own list.
  useEffect(() => {
    if (!open || workers) return
    let cancelled = false
    fetchWorkers()
      .then((list) => { if (!cancelled) setFetched(list) })
      .catch(() => { /* the picker simply stays empty */ })
    return () => { cancelled = true }
  }, [open, workers])

  const sectionLabel: React.CSSProperties = {
    color: colors.detailLabel,
    fontSize: fontSizes.sm,
    textTransform: 'uppercase',
    letterSpacing: '0.5px',
    marginTop: 12,
    marginBottom: 2,
  }
  const hint: React.CSSProperties = { fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }
  const summary = publish.length + subscribe.length

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, minHeight: 24 }}>
        <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{t('workers.create.allow')}</span>
        <span onClick={() => setOpen(true)} className="btn-hover" style={{ ...btn.accent, borderRadius: 3, padding: '3px 12px' }}>
          {t('workers.create.allow.button')}
        </span>
        {summary > 0 && (
          <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>
            {t('workers.create.allow.summary', { p: publish.length, s: subscribe.length })}
          </span>
        )}
      </div>

      {open && createPortal(
        <>
          <div onClick={() => setOpen(false)} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', zIndex: 95 }} />
          <div
            onClick={(e) => e.stopPropagation()}
            role="dialog"
            style={{
              position: 'fixed',
              top: '50%',
              left: '50%',
              transform: 'translate(-50%, -50%)',
              width: 'min(720px, calc(100vw - 32px))',
              maxHeight: '80vh',
              display: 'flex',
              flexDirection: 'column',
              overflow: 'hidden',
              background: colors.bg,
              border: '1px solid ' + colors.border,
              borderRadius: 6,
              boxShadow: '0 8px 24px rgba(0,0,0,0.25)',
              zIndex: 96,
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '12px 16px 10px', borderBottom: '1px solid ' + colors.border }}>
              <span style={{ color: colors.text, fontSize: fontSizes.md }}>{t('workers.create.allow')}</span>
              <span
                onClick={() => setOpen(false)}
                className="btn-hover"
                style={{ cursor: 'pointer', marginLeft: 'auto', border: '1px solid ' + colors.border, borderRadius: 2, padding: '0 8px', color: colors.textDim, fontSize: fontSizes.sm, lineHeight: '20px', userSelect: 'none' }}
              >
                {'\u2715'}
              </span>
            </div>

            <div style={{ overflowY: 'auto', flex: 1, minHeight: 0, padding: '0 16px 14px' }}>
              <div style={sectionLabel}>{t('wd.publishAllow')}</div>
              <PublishAllowRows rows={publish} onChange={onPublishChange} allWorkers={allWorkers} />
              <div style={hint}>{t('workers.create.publish.default')}</div>

              <div style={sectionLabel}>{t('wd.subscribeAllow')}</div>
              <SubscribeAllowRows rows={subscribe} onChange={onSubscribeChange} allWorkers={allWorkers} />
              <div style={hint}>{t('workers.create.subscriptions.default')}</div>
            </div>

            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', padding: '10px 16px', borderTop: '1px solid ' + colors.border }}>
              <span onClick={() => setOpen(false)} className="btn-hover" style={btn.ghost}>{t('wd.done')}</span>
            </div>
          </div>
        </>,
        document.body,
      )}
    </div>
  )
}
