import { useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { usePolling } from '../hooks/usePolling'
import { CONTROL, fetchTemplates, fetchTemplate, createTemplate, deleteTemplate } from '../services/api'

// TemplatesView manages the project template set (list, clone, delete) with a
// detail panel: clicking a template shows its JSON definition. All calls go to
// the control plane on :9527.
export default function TemplatesView() {
  const { colors } = useTheme()
  const [templates, setTemplates] = useState<string[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [detail, setDetail] = useState<any>(null)
  const [newId, setNewId] = useState('')
  const [copyFrom, setCopyFrom] = useState('')
  const [error, setError] = useState('')

  usePolling<string[]>(CONTROL + '/api/templates', 5000, setTemplates, true)

  useEffect(() => {
    if (!selected) { setDetail(null); return }
    fetchTemplate(selected)
      .then(setDetail)
      .catch(() => setDetail(null))
  }, [selected])

  const select = (name: string) => {
    setSelected(name)
    setCopyFrom(name)
  }

  const create = async () => {
    const id = newId.trim()
    if (!id || !copyFrom) { setError('a name and a source template are required'); return }
    setError('')
    try {
      await createTemplate(id, copyFrom)
      setNewId('')
      setSelected(id)
    } catch (e) {
      setError('failed to create template ' + id)
    }
  }

  const remove = async (name: string) => {
    setError('')
    try {
      await deleteTemplate(name)
      if (selected === name) { setSelected(null); setDetail(null) }
    } catch (e) {
      setError('failed to delete template ' + name)
    }
  }

  const source = copyFrom || (templates[0] || '')

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', overflow: 'hidden' }}>
      {/* Left: list + clone */}
      <div style={{ width: 320, minWidth: 320, padding: 24, overflowY: 'auto', borderRight: '1px solid ' + colors.border }}>
        <h3 style={{ margin: '0 0 16px', color: colors.accent, fontSize: fontSizes.xl }}>Templates</h3>

        {/* Clone a template */}
        <div style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '12px', marginBottom: 16, display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <input
            value={newId}
            onChange={(e) => setNewId(e.target.value)}
            placeholder="new template id"
            style={{ flex: 1, minWidth: 120, padding: '6px 10px', fontSize: fontSizes.md, background: colors.bgLight, border: '1px solid ' + colors.border, color: colors.text, outline: 'none' }}
          />
          {templates.length > 0 && (
            <select
              value={source}
              onChange={(e) => setCopyFrom(e.target.value)}
              style={{ padding: '6px 8px', fontSize: fontSizes.md, background: colors.bgLight, color: colors.text, border: '1px solid ' + colors.border }}
            >
              {templates.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
          )}
          <button
            onClick={create}
            style={{ cursor: 'pointer', background: colors.accent, color: '#fff', border: 'none', borderRadius: 4, padding: '6px 14px', fontSize: fontSizes.sm }}
          >
            Clone
          </button>
        </div>

        {error && <div style={{ color: colors.toolFailed, marginBottom: 12, fontSize: fontSizes.sm }}>{error}</div>}

        {templates.length === 0 ? (
          <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>No templates.</div>
        ) : (
          templates.map((t) => (
            <div
              key={t}
              onClick={() => select(t)}
              style={{
                border: '1px solid ' + (selected === t ? colors.accent : colors.border),
                borderRadius: 6,
                padding: '10px 14px',
                marginBottom: 8,
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                cursor: 'pointer',
              }}
            >
              <div style={{ flex: 1, color: selected === t ? colors.accent : colors.text, fontSize: fontSizes.md }}>{t}</div>
              <button
                onClick={(e) => { e.stopPropagation(); remove(t) }}
                style={{ cursor: 'pointer', background: 'transparent', color: colors.toolFailed, border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 10px', fontSize: fontSizes.sm }}
              >
                Delete
              </button>
            </div>
          ))
        )}
      </div>

      {/* Right: detail panel */}
      <div style={{ flex: 1, minWidth: 0, padding: 24, overflowY: 'auto' }}>
        {selected ? (
          <>
            <h3 style={{ margin: '0 0 6px', color: colors.accent, fontSize: fontSizes.lg }}>{selected}</h3>
            {Array.isArray(detail?.workers) ? (
              <>
                <div style={{ color: colors.textDim, fontSize: fontSizes.sm, marginBottom: 14 }}>{detail.workers.length} worker(s)</div>
                {detail.workers.map((w: any, i: number) => (
                  <div key={i} style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '10px 14px', marginBottom: 8 }}>
                    <div style={{ color: colors.text, fontSize: fontSizes.md }}>
                      <span style={{ color: colors.accent, fontFamily: 'monospace' }}>{w.type}</span>
                      <span style={{ color: colors.textDim }}> · {w.id}</span>
                    </div>
                    {(w.subscriptions && w.subscriptions.length > 0) && (
                      <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginTop: 4, fontFamily: 'monospace', wordBreak: 'break-all' }}>
                        {w.subscriptions.map((s: any) => (typeof s === 'string' ? s : (s.source ? `${s.type} ← ${s.source}` : s.type))).join(', ')}
                      </div>
                    )}
                  </div>
                ))}
              </>
            ) : (
              <div style={{ color: colors.textDimmed, fontFamily: 'monospace', whiteSpace: 'pre-wrap', fontSize: fontSizes.sm }}>
                {JSON.stringify(detail, null, 2) || '—'}
              </div>
            )}
          </>
        ) : (
          <div style={{ color: colors.textDimmed, fontSize: fontSizes.md }}>Select a template to see its definition.</div>
        )}
      </div>
    </div>
  )
}