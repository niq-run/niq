import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { fetchWorkers } from '../services/api'
import type { WorkerInfo } from '../types'
import { PublishAllowRows, SubscribeAllowRows, type PublishRow, type SubscribeRow } from './AllowEditors'

interface PermissionPanelProps {
  publish: PublishRow[]
  subscribe: SubscribeRow[]
  onPublishChange: (rows: PublishRow[]) => void
  onSubscribeChange: (rows: SubscribeRow[]) => void
  // Live worker list, used by the "pick a worker's published events" helper in
  // the subscription editor. When not supplied (the template builder has no
  // worker list of its own) the panel fetches it on first expand.
  workers?: WorkerInfo[]
  defaultOpen?: boolean
}

// PermissionPanel is the create form's "订阅发布管理" module: collapsed to a
// single 权限管理 button, expanding into the same publish/subscribe row editors
// the worker detail's permission tab uses. Rows are draft state owned by the
// caller, so the panel works both for a worker being created and for a
// template's worker entry being built.
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
  const rootRef = useRef<HTMLDivElement>(null)
  const wasOpen = useRef(open)

  // Expanding reveals a form taller than the dialog's visible area, so scroll
  // its scroll container to the bottom once the rows are in the DOM — the
  // freshly revealed controls would otherwise sit below the fold.
  useEffect(() => {
    if (open && !wasOpen.current) {
      requestAnimationFrame(() => rootRef.current?.scrollIntoView({ block: 'end' }))
    }
    wasOpen.current = open
  }, [open])

  // Fetch the worker list lazily: only once the form is opened AND the caller
  // did not provide one. A cancelled flag keeps a slow reply from landing after
  // the panel closed or the caller switched to its own list.
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

  return (
    <div ref={rootRef}>
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, minHeight: 24 }}>
        <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{t('workers.create.allow')}</span>
        <span
          onClick={() => setOpen((v) => !v)}
          className="btn-hover"
          style={{
            cursor: 'pointer',
            border: '1px solid ' + (open ? colors.border : colors.accent),
            borderRadius: 3,
            padding: '3px 12px',
            color: open ? colors.textDim : colors.accent,
            fontSize: fontSizes.sm,
            userSelect: 'none',
          }}
        >
          {open ? t('workers.create.allow.collapse') : t('workers.create.allow.button')}
        </span>
        {!open && publish.length + subscribe.length > 0 && (
          <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>
            {t('workers.create.allow.summary', { p: publish.length, s: subscribe.length })}
          </span>
        )}
      </div>

      {open && (
        <div>
          <div style={sectionLabel}>{t('wd.publishAllow')}</div>
          <PublishAllowRows rows={publish} onChange={onPublishChange} allWorkers={allWorkers} />
          <div style={hint}>{t('workers.create.publish.default')}</div>

          <div style={sectionLabel}>{t('wd.subscribeAllow')}</div>
          <SubscribeAllowRows rows={subscribe} onChange={onSubscribeChange} allWorkers={allWorkers} />
          <div style={hint}>{t('workers.create.subscriptions.default')}</div>
        </div>
      )}
    </div>
  )
}
