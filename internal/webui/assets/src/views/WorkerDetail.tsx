import { useCallback, useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo, type ProviderOption, type ProviderSelection } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'
import { suspendWorker, resumeWorker, startWorker, stopWorker, restartWorker, deleteWorker, fetchWorkerProviders, switchWorkerProvider, updateWorkerAllow } from '../services/api'

interface WorkerDetailProps {
  worker: WorkerInfo
  allWorkers: WorkerInfo[]
  onClose: () => void
  archived: Set<string>
  onToggleArchived: (id: string) => void
  onDeleted: (id: string) => void
}

export default function WorkerDetail({ worker, allWorkers, onClose, archived, onToggleArchived, onDeleted }: WorkerDetailProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const suspended = worker.managed && worker.state === 'suspended'
  const isArchived = archived.has(worker.id)
  const connection = worker.online === false ? t('worker.offline') : t('worker.online')
  const lifecycle = worker.managed ? (suspended ? t('worker.suspended') : t('worker.running')) : '\u2014'
  const typeColor = worker.type ? getWorkerTypeColor(worker.type, colors) : colors.textDimmed
  const [editingSub, setEditingSub] = useState(false)
  const [subNote, setSubNote] = useState('')
  const [umBusy, setUmBusy] = useState('')
  const [umNote, setUmNote] = useState('')
  const [confirmDel, setConfirmDel] = useState(false)
  const [delBusy, setDelBusy] = useState(false)

  const doDelete = async () => {
    setDelBusy(true)
    try {
      await deleteWorker(worker.id)
      onDeleted(worker.id)
    } catch (e) {
      setUmNote((e as Error)?.message || 'delete failed')
      setConfirmDel(false)
    } finally {
      setDelBusy(false)
    }
  }

  const unmanagedAction = async (op: 'start' | 'stop' | 'restart') => {
    setUmBusy(op)
    setUmNote('')
    try {
      if (op === 'start') await startWorker(worker.id)
      else if (op === 'stop') await stopWorker(worker.id)
      else await restartWorker(worker.id)
      setUmNote({ start: t('wd.starting'), stop: t('wd.stopping'), restart: t('wd.restarting') }[op])
    } catch (e) {
      setUmNote((e as Error)?.message || op + ' failed')
    } finally {
      setUmBusy('')
    }
  }

  const handleAction = async () => {
    if (suspended) await resumeWorker(worker.id)
    else await suspendWorker(worker.id)
  }

  const tag: React.CSSProperties = {
    display: 'inline-block',
    padding: '0 6px',
    borderRadius: 4,
    fontSize: fontSizes.sm,
    lineHeight: '18px',
    color: typeColor,
    background: typeColor + '1f',
    border: '1px solid ' + typeColor + '55',
  }

  return (
    <div style={{ flex: 1, width: '100%', minWidth: 0, overflowY: 'auto', fontSize: fontSizes.base, background: colors.bg }}>
      {/* Sticky header — full width so content scrolling underneath is covered */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          flexWrap: 'wrap',
          position: 'sticky',
          top: 0,
          zIndex: 1,
          background: colors.bg,
          borderBottom: '1px solid ' + colors.border,
          padding: '12px 14px 8px',
        }}
      >
        <span style={{ color: colors.text, fontSize: fontSizes.md }}>{worker.id}</span>
        {worker.type && <span style={tag}>{worker.type}</span>}
        <span
          onClick={onClose}
          className="btn-hover"
          title={t('detail.close.tooltip')}
          style={{
            cursor: 'pointer',
            marginLeft: 'auto',
            border: '1px solid ' + colors.border,
            borderRadius: 4,
            padding: '0 8px',
            color: colors.textDim,
            fontSize: fontSizes.md,
            lineHeight: '20px',
            userSelect: 'none',
          }}
        >
          {'\u2715'}
        </span>
      </div>

      {/* Scrollable content */}
      <div style={{ padding: 14 }}>
        <div style={{ padding: '10px 12px', background: colors.detailBg, borderRadius: 6, fontSize: fontSizes.base }}>
          <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 16px', alignItems: 'baseline' }}>
            <DetailRow label={t('wd.id')} value={worker.id} colors={colors} />
            <DetailRow label={t('wd.type')} value={worker.type || '(none)'} colors={colors} />
            <DetailRow label={t('wd.connection')} value={connection} colors={colors} />
            <DetailRow label={t('wd.lifecycle')} value={lifecycle} colors={colors} />
            <DetailRow label={t('wd.managed')} value={worker.managed ? t('wd.yes') : t('wd.no')} colors={colors} />
            <DetailRow label={t('wd.credential')} value={worker.credential || '(none)'} colors={colors} />
          </div>

          <div style={{ marginTop: 18, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 16 }}>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              {t('wd.publishAllow')}
            </div>
            <AllowTags items={(worker.publish_allow || []).map((p) => ({ label: p.type + (p.target_worker_id ? ' → ' + p.target_worker_id : ''), title: p.type + (p.target_worker_id ? ' → ' + p.target_worker_id : '') }))} colors={colors} />
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, margin: '16px 0 8px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              {t('wd.subscribeAllow')}
              <span
                onClick={() => { setEditingSub((v) => !v); setSubNote('') }}
                style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, textTransform: 'none', letterSpacing: 0, marginLeft: 10, userSelect: 'none' }}
              >
                {editingSub ? t('wd.close') : t('wd.edit')}
              </span>
            </div>
            {editingSub ? (
              <SubscribeAllowEditor key="edit" worker={worker} allWorkers={allWorkers} onDone={(note) => { setEditingSub(false); setSubNote(note) }} />
            ) : (
              <SubscribeAllowEditor key="view" worker={worker} allWorkers={allWorkers} readOnly />
            )}
            {subNote && (
              <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>{subNote}</div>
            )}
          </div>

          {/* Action area, separated by a horizontal line */}
          <div style={{ marginTop: 18, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 18 }}>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 10, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              {t('wd.actions')}
            </div>
            {worker.managed ? (
              <>
                <div style={{ marginBottom: 22 }}>
                  <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginBottom: 6, lineHeight: 1.5 }}>
                    {suspended
                      ? t('wd.resume.desc')
                      : t('wd.suspend.desc')}
                  </div>
                  <span
                    onClick={handleAction}
                    className="btn-hover"
                    style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {suspended ? t('wd.resume') : t('wd.suspend')}
                  </span>
                </div>
                <div>
                  <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginBottom: 6, lineHeight: 1.5 }}>
                    {isArchived
                      ? t('wd.restore.desc')
                      : t('wd.archive.desc')}
                  </div>
                  <span
                    onClick={() => onToggleArchived(worker.id)}
                    className="btn-hover"
                    style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {isArchived ? t('wd.restore') : t('wd.archive')}
                  </span>
                </div>
              </>
            ) : worker.unmanaged ? (
              <div>
                <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginBottom: 6, lineHeight: 1.5 }}>
                  {worker.unmanaged_state === 'running'
                    ? t('wd.unmanaged.running.desc')
                    : t('wd.unmanaged.start.desc')}
                </div>
                {worker.unmanaged_state === 'running' ? (
                  <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
                    <span
                      onClick={() => unmanagedAction('stop')}
                      className="btn-hover"
                      style={{ cursor: umBusy ? 'default' : 'pointer', opacity: umBusy ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                    >
                      {umBusy === 'stop' ? t('wd.stopping') : t('wd.stop')}
                    </span>
                    <span
                      onClick={() => unmanagedAction('restart')}
                      className="btn-hover"
                      style={{ cursor: umBusy ? 'default' : 'pointer', opacity: umBusy ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                    >
                      {umBusy === 'restart' ? t('wd.restarting') : t('wd.restart')}
                    </span>
                  </div>
                ) : (
                  <span
                    onClick={() => unmanagedAction('start')}
                    className="btn-hover"
                    style={{ cursor: umBusy ? 'default' : 'pointer', opacity: umBusy ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {umBusy === 'start' ? t('wd.starting') : t('wd.start')}
                  </span>
                )}
                {umNote && (
                  <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>{umNote}</div>
                )}
              </div>
            			) : (
            				<span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('wd.notManaged')}</span>
            			)}

            			{/* Danger: permanently remove this worker. */}
            			<div style={{ marginTop: 18, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 16 }}>
				<div style={{ color: colors.detailLabel, fontSize: fontSizes.sm, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
					{t('wd.delete')}
				</div>
            				{confirmDel ? (
            					<div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }}>
            						<span style={{ color: colors.textDim, fontSize: fontSizes.sm }}>{t('wd.delete.confirm', { id: worker.id })}</span>
            						<span
            							onClick={doDelete}
            							className="btn-hover"
            							style={{ cursor: delBusy ? 'default' : 'pointer', opacity: delBusy ? 0.6 : 1, display: 'inline-block', border: '1px solid #' + 'c33', borderRadius: 4, padding: '4px 12px', color: '#c33', fontSize: fontSizes.md, userSelect: 'none' }}
            						>
            							{delBusy ? t('wd.deleting') : t('wd.confirmDelete')}
            						</span>
            						<span
            							onClick={() => setConfirmDel(false)}
            							className="btn-hover"
            							style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
            						>
            							{t('wd.cancel')}
            						</span>
            					</div>
            				) : (
            					<span
            						onClick={() => { setConfirmDel(true); setUmNote('') }}
            						className="btn-hover"
            						style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid #' + 'c33', borderRadius: 4, padding: '4px 12px', color: '#c33', fontSize: fontSizes.md, userSelect: 'none' }}
            					>
            						{t('wd.delete')}
            					</span>
            				)}
            			</div>
            		  </div>

            		  {/* Model / Provider — only reason workers carry ProviderSources.
            			  Keyed by worker id so switching workers cannot leak one worker's
              provider state into another. */}
          {worker.type === 'reason' && <ProviderSection key={worker.id} workerId={worker.id} />}
        </div>
      </div>
    </div>
  )
}

// ProviderSection is a bespoke model switcher for a reason worker: it asks the
// worker itself for its selectable providers (the provider.list event) and
// switches the active pair (the provider.switch event). A later pass will
// generalise this from the worker's declared capabilities into a form.
function ProviderSection({ workerId }: { workerId: string }) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [expanded, setExpanded] = useState(false)
  const [providers, setProviders] = useState<ProviderOption[]>([])
  const [current, setCurrent] = useState<ProviderSelection>({ provider: '', model: '' })
  const [selProvider, setSelProvider] = useState('')
  const [selModel, setSelModel] = useState('')
  const [loading, setLoading] = useState(false)
  const [switching, setSwitching] = useState(false)
  const [doneNote, setDoneNote] = useState('')
  const [error, setError] = useState('')

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    setError('')
    try {
      const res = await fetchWorkerProviders(workerId, signal)
      const list = res.providers || []
      const cur = res.current || { provider: '', model: '' }
      // Fall back to the first provider when the worker reports no current
      // choice, so the selects always show something concrete instead of an
      // empty selection that also reads as "current" (and so stays disabled).
      const provider = cur.provider || list[0]?.name || ''
      const opt = list.find((p) => p.name === provider)
      setProviders(list)
      setCurrent(cur)
      setSelProvider(provider)
      setSelModel(cur.model || opt?.default || opt?.models?.[0] || '')
    } catch (e) {
      if ((e as Error)?.name === 'AbortError') return
      setError((e as Error)?.message || 'failed to load providers')
    } finally {
      setLoading(false)
    }
  }, [workerId])

  // Fetched lazily on first expand, not on mount: the answer comes from the
  // worker over the bus and can take seconds, so it must not fire for every
  // worker selection (the worker list also re-polls every few seconds).
  useEffect(() => {
    if (!expanded) return
    const ac = new AbortController()
    load(ac.signal)
    return () => ac.abort()
  }, [expanded, load])

  const selectStyle: React.CSSProperties = {
    width: '100%',
    maxWidth: 260,
    padding: '6px 8px',
    fontSize: fontSizes.md,
    background: colors.bgLight,
    color: colors.text,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
  }

  const active = providers.find((p) => p.name === selProvider)
  const models = active?.models ?? []

  const pickProvider = (name: string) => {
    const opt = providers.find((p) => p.name === name)
    setSelProvider(name)
    setSelModel(opt?.default || opt?.models?.[0] || '')
    setDoneNote('')
  }

  const apply = async () => {
    setSwitching(true)
    setError('')
    setDoneNote('')
    try {
      const res = await switchWorkerProvider(workerId, selProvider, selModel)
      if (!res.done) {
        setError(res.error || 'the worker refused the switch')
        return
      }
      setDoneNote(t('wd.switched'))
      await load()
    } catch (e) {
      setError((e as Error)?.message || 'provider switch failed')
    } finally {
      setSwitching(false)
    }
  }

  const unchanged = selProvider === current.provider && selModel === current.model

  return (
    <div style={{ marginTop: 18, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 18 }}>
      <div
        onClick={() => setExpanded((v) => !v)}
        style={{ cursor: 'pointer', display: 'flex', alignItems: 'baseline', gap: 8, userSelect: 'none' }}
      >
        <span style={{ color: colors.detailLabel, fontSize: fontSizes.base, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
          {t('wd.modelProvider')}
        </span>
        <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{expanded ? '−' : '+'}</span>
        {!expanded && current.provider && (
          <span style={{ color: colors.detailValue, fontSize: fontSizes.sm, marginLeft: 'auto' }}>
            {current.provider} · {current.model}
          </span>
        )}
      </div>

      {expanded && (
        <div style={{ marginTop: 12 }}>
          {loading && (
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, color: colors.textDimmed, fontSize: fontSizes.sm }}>
              <span className="niq-spinner" style={{ width: 13, height: 13, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />
              {t('wd.askingProviders')}
            </div>
          )}

          {!loading && error && (
            <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{error}</div>
          )}

          {!loading && !error && providers.length === 0 && (
            <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('wd.noProviders')}</div>
          )}

          {!loading && !error && providers.length > 0 && (
            <>
              <div style={{ marginBottom: 10 }}>
                <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginBottom: 4 }}>{t('wd.provider')}</div>
                <select value={selProvider} onChange={(e) => pickProvider(e.target.value)} style={selectStyle}>
                  {providers.map((p) => (
                    <option key={p.name} value={p.name} style={{ background: colors.bgLight, color: colors.text }}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </div>

              <div style={{ marginBottom: 10 }}>
                <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginBottom: 4 }}>{t('wd.model')}</div>
                <select value={selModel} onChange={(e) => { setSelModel(e.target.value); setDoneNote('') }} style={selectStyle}>
                  {(models.includes(selModel) ? models : [selModel, ...models]).filter(Boolean).map((m) => (
                    <option key={m} value={m} style={{ background: colors.bgLight, color: colors.text }}>
                      {m}
                    </option>
                  ))}
                </select>
              </div>

              <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
                <span
                  onClick={() => { if (!switching && !unchanged) apply() }}
                  className="btn-hover"
                  style={{
                    cursor: switching || unchanged ? 'default' : 'pointer',
                    display: 'inline-block',
                    border: '1px solid ' + colors.border,
                    borderRadius: 4,
                    padding: '4px 12px',
                    color: unchanged ? colors.textDimmed : colors.textDim,
                    fontSize: fontSizes.md,
                    opacity: switching ? 0.6 : 1,
                    userSelect: 'none',
                  }}
                >
                  {switching ? t('wd.updating') : unchanged ? t('wd.current') : t('wd.updateProvider')}
                </span>
                {switching && (
                  <span className="niq-spinner" style={{ width: 13, height: 13, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />
                )}
              </div>

              <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>
                {doneNote || t('wd.updateProvider.hint')}
              </div>
            </>
          )}
        </div>
      )}
    </div>
  )
}

// SubscribeAllowEditor renders the SubscribeAllow list in the same row layout
// as the editor: one row per pattern, each with a type field and an optional
// source field. In readOnly mode it shows the worker's current list with the
// fields disabled; in edit mode it drafts a replacement and saves it against
// the bus registry. Saving replaces the whole list; the worker list polls and
// refreshes the read-only view after the round trip.
function SubscribeAllowEditor({ worker, allWorkers = [], readOnly = false, onDone }: { worker: WorkerInfo; allWorkers?: WorkerInfo[]; readOnly?: boolean; onDone?: (note: string) => void }) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [draft, setDraft] = useState<{ type: string; source: string }[]>(() =>
    (worker.subscribe_allow || []).map((p) => ({ type: p.type, source: p.source_id || '' })),
  )
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  // Read-only view tracks the worker prop directly (the draft would go stale
  // as the list polls); edit mode drafts on its own state.
  const rows = readOnly
    ? (worker.subscribe_allow || []).map((p) => ({ type: p.type, source: p.source_id || '' }))
    : draft

  const inputStyle: React.CSSProperties = {
    flex: 1,
    minWidth: 0,
    padding: '5px 8px',
    fontSize: fontSizes.sm,
    background: colors.bgLight,
    color: colors.text,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    opacity: readOnly ? 0.75 : 1,
  }

  const save = async () => {
    const valid = draft.filter((r) => r.type.trim() !== '')
    if (valid.length === 0 && draft.length > 0) {
      setError(t('wd.rowNeedsType'))
      return
    }
    setSaving(true)
    setError('')
    try {
      await updateWorkerAllow(worker.id, {
        subscribe_allow: valid.map((r) =>
          r.source.trim() ? { type: r.type.trim(), source_id: r.source.trim() } : { type: r.type.trim() },
        ),
      })
      onDone?.(t('wd.saved'))
    } catch (e) {
      setError((e as Error)?.message || 'save failed')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div style={{ marginTop: 6 }}>
      {rows.map((r, i) => (
        <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 6, alignItems: 'center' }}>
          <input
            value={r.type}
            placeholder={readOnly ? undefined : t('wd.subPlaceholder')}
            disabled={readOnly}
            onChange={(e) => { const n = [...draft]; n[i] = { ...n[i], type: e.target.value }; setDraft(n) }}
            style={inputStyle}
          />
          <select
            value={r.source}
            disabled={readOnly}
            onChange={(e) => { const n = [...draft]; n[i] = { ...n[i], source: e.target.value }; setDraft(n) }}
            style={{ ...inputStyle, flex: '0 1 170px', appearance: 'none' }}
          >
            <option value="" style={{ background: colors.bgLight, color: colors.text }}>{t('wd.anySource')}</option>
            {r.source !== '' && !allWorkers.some((w) => w.id === r.source) && (
              <option value={r.source} style={{ background: colors.bgLight, color: colors.text }}>{r.source}</option>
            )}
            {allWorkers.map((w) => (
              <option key={w.id} value={w.id} style={{ background: colors.bgLight, color: colors.text }}>{w.id}</option>
            ))}
          </select>
          {!readOnly && (
            <span
              onClick={() => setDraft(draft.filter((_, j) => j !== i))}
              className="btn-hover"
              title={t('wd.remove')}
              style={{ cursor: 'pointer', color: colors.textDimmed, fontSize: fontSizes.md, userSelect: 'none', padding: '0 4px' }}
            >
              {'\u2715'}
            </span>
          )}
        </div>
      ))}
      {!readOnly && (
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', marginTop: 8, flexWrap: 'wrap' }}>
          <span
            onClick={() => setDraft([...draft, { type: '', source: '' }])}
            className="btn-hover"
            style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '3px 10px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {t('wd.add')}
          </span>
          <span
            onClick={save}
            className="btn-hover"
            style={{ cursor: saving ? 'default' : 'pointer', opacity: saving ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '3px 10px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {saving ? t('wd.saving') : t('wd.save')}
          </span>
          {error && <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{error}</span>}
        </div>
      )}
      {rows.length === 0 && (
        <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 8, lineHeight: 1.5 }}>
          {t('wd.noSubscriptions')}
        </div>
      )}
    </div>
  )
}

// AllowTags renders the publish/subscribe allow lists as a wrapping row of
// low-contrast chips. Both lists can be long, so they read as rows of labels
// rather than competing with the worker's identity and type badges above.
function AllowTags({ items, colors }: { items: { label: string; title: string }[]; colors: import('../theme').Palette }) {
  if (items.length === 0) {
    return <span style={{ color: colors.detailValue, fontSize: fontSizes.base }}>{'\u2014'}</span>
  }
  return (
    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6, lineHeight: 1.7 }}>
      {items.map((it, i) => (
        <span
          key={i}
          title={it.title}
          style={{
            display: 'inline-block',
            padding: '0 5px',
            borderRadius: 3,
            fontSize: fontSizes.xs,
            lineHeight: '20px',
            whiteSpace: 'nowrap',
            color: colors.textDim,
            background: colors.bg,
            border: '1px solid ' + colors.detailBorder,
          }}
        >
          {it.label}
        </span>
      ))}
    </div>
  )
}

function DetailRow({ label, value, colors }: { label: string; value: string; colors: import('../theme').Palette }) {
  return (
    <>
      <span style={{ color: colors.detailLabel, fontSize: fontSizes.base }}>{label}</span>
      <span style={{ color: colors.detailValue, fontSize: fontSizes.base, wordBreak: 'break-all' }}>{value}</span>
    </>
  )
}
