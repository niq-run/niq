import { useCallback, useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { type WorkerInfo, type ProviderOption, type ProviderSelection } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'
import { suspendWorker, resumeWorker, fetchWorkerProviders, switchWorkerProvider } from '../services/api'

interface WorkerDetailProps {
  worker: WorkerInfo
  onClose: () => void
  archived: Set<string>
  onToggleArchived: (id: string) => void
}

export default function WorkerDetail({ worker, onClose, archived, onToggleArchived }: WorkerDetailProps) {
  const { colors } = useTheme()
  const suspended = worker.managed && worker.state === 'suspended'
  const isArchived = archived.has(worker.id)
  const connection = worker.online === false ? 'offline' : 'online'
  const lifecycle = worker.managed ? (suspended ? 'suspended' : 'running') : '\u2014'
  const typeColor = worker.type ? getWorkerTypeColor(worker.type, colors) : colors.textDimmed

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
          title="close"
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
            <DetailRow label="ID" value={worker.id} colors={colors} />
            <DetailRow label="Type" value={worker.type || '(none)'} colors={colors} />
            <DetailRow label="Connection" value={connection} colors={colors} />
            <DetailRow label="Lifecycle" value={lifecycle} colors={colors} />
            <DetailRow label="Managed" value={worker.managed ? 'yes' : 'no'} colors={colors} />
            <DetailRow label="Credential" value={worker.credential || '(none)'} colors={colors} />
          </div>

          <div style={{ marginTop: 18, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 16 }}>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
              Publish Allow
            </div>
            <AllowTags items={(worker.publish_allow || []).map((t) => ({ label: t, title: t }))} colors={colors} />
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, margin: '16px 0 8px', textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
              Subscribe Allow
            </div>
            <AllowTags
              items={(worker.subscribe_allow || []).map((p) => ({
                label: p.source_id ? `${p.type}@${p.source_id}` : p.type,
                title: p.source_id ? `${p.type} from ${p.source_id}` : p.type,
              }))}
              colors={colors}
            />
          </div>

          {/* Action area, separated by a horizontal line */}
          <div style={{ marginTop: 18, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 18 }}>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 10, textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
              Actions
            </div>
            {worker.managed ? (
              <>
                <div style={{ marginBottom: 22 }}>
                  <span
                    onClick={handleAction}
                    className="btn-hover"
                    style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {suspended ? 'resume' : 'suspend'}
                  </span>
                  <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>
                    {suspended
                      ? 'Resume the worker: reconnect it to the bus and restart it from its last snapshot.'
                      : 'Suspend the worker: stop it and release its bus connection (state is kept on disk).'}
                  </div>
                </div>
                <div>
                  <span
                    onClick={() => onToggleArchived(worker.id)}
                    className="btn-hover"
                    style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
                  >
                    {isArchived ? 'restore' : 'archive'}
                  </span>
                  <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>
                    {isArchived
                      ? 'Restore the worker: show it again in the worker selector.'
                      : 'Archive the worker: hide it from the worker selector until you restore it here.'}
                  </div>
                </div>
              </>
            ) : (
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>not host-managed</span>
            )}
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
// worker itself for its selectable providers (worker.query provider.list) and
// switches the active pair (worker.update provider.switch). A later pass will
// generalise this from the worker's declared capabilities into a form.
function ProviderSection({ workerId }: { workerId: string }) {
  const { colors } = useTheme()
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
      setDoneNote('switched — takes effect from the next reasoning round')
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
        <span style={{ color: colors.detailLabel, fontSize: fontSizes.base, textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
          Model / Provider
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
              asking the worker for its providers…
            </div>
          )}

          {!loading && error && (
            <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{error}</div>
          )}

          {!loading && !error && providers.length === 0 && (
            <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>this worker reports no switchable providers</div>
          )}

          {!loading && !error && providers.length > 0 && (
            <>
              <div style={{ marginBottom: 10 }}>
                <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginBottom: 4 }}>Provider</div>
                <select value={selProvider} onChange={(e) => pickProvider(e.target.value)} style={selectStyle}>
                  {providers.map((p) => (
                    <option key={p.name} value={p.name} style={{ background: colors.bgLight, color: colors.text }}>
                      {p.name}
                    </option>
                  ))}
                </select>
              </div>

              <div style={{ marginBottom: 10 }}>
                <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginBottom: 4 }}>Model</div>
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
                  {switching ? 'Updating…' : unchanged ? 'current' : 'update provider'}
                </span>
                {switching && (
                  <span className="niq-spinner" style={{ width: 13, height: 13, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />
                )}
              </div>

              <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>
                {doneNote || 'Update the worker’s active provider and model.'}
              </div>
            </>
          )}
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
