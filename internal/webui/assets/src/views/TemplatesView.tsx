import { useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { usePolling } from '../hooks/usePolling'
import { CONTROL, fetchTemplates, fetchTemplate, createTemplate, deleteTemplate } from '../services/api'

// TemplatesView manages the project template set (list, clone, delete) with a
// detail panel: clicking a template shows its JSON definition. All calls go to
// the control plane on :9527.
export default function TemplatesView() {
  const { colors } = useTheme()
  const { t } = useI18n()
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
    if (!id || !copyFrom) { setError(t('templates.error.required')); return }
    setError('')
    try {
      await createTemplate(id, copyFrom)
      setNewId('')
      setSelected(id)
    } catch (e) {
      setError(t('templates.error.create', { id }))
    }
  }

  const remove = async (name: string) => {
    setError('')
    try {
      await deleteTemplate(name)
      if (selected === name) { setSelected(null); setDetail(null) }
    } catch (e) {
      setError(t('templates.error.delete', { id: name }))
    }
  }

  const source = copyFrom || (templates[0] || '')

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', overflow: 'hidden' }}>
      {/* Left: list + clone */}
      <div style={{ width: 320, minWidth: 320, padding: 24, overflowY: 'auto', borderRight: '1px solid ' + colors.border }}>
        <h3 style={{ margin: '0 0 16px', color: colors.accent, fontSize: fontSizes.xl }}>{t('sidebar.templates')}</h3>

        {/* Clone a template */}
        <div style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '12px', marginBottom: 16, display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          <input
            value={newId}
            onChange={(e) => setNewId(e.target.value)}
            placeholder={t('templates.newId.placeholder')}
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
            {t('templates.clone')}
          </button>
        </div>

        {error && <div style={{ color: colors.toolFailed, marginBottom: 12, fontSize: fontSizes.sm }}>{error}</div>}

        {templates.length === 0 ? (
          <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>{t('templates.empty')}</div>
        ) : (
          templates.map((tmpl) => (
            <div
              key={tmpl}
              onClick={() => select(tmpl)}
              style={{
                border: '1px solid ' + (selected === tmpl ? colors.accent : colors.border),
                borderRadius: 6,
                padding: '10px 14px',
                marginBottom: 8,
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                cursor: 'pointer',
              }}
            >
              <div style={{ flex: 1, color: selected === tmpl ? colors.accent : colors.text, fontSize: fontSizes.md }}>{tmpl}</div>
              <button
                onClick={(e) => { e.stopPropagation(); remove(tmpl) }}
                style={{ cursor: 'pointer', background: 'transparent', color: colors.toolFailed, border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 10px', fontSize: fontSizes.sm }}
              >
                {t('templates.delete')}
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
                <div style={{ color: colors.textDim, fontSize: fontSizes.sm, marginBottom: 14 }}>{t('templates.workerCount', { n: detail.workers.length })}</div>
                {detail.workers.map((w: any, i: number) => (
                  <div key={i} style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '10px 14px', marginBottom: 8 }}>
                    <div style={{ color: colors.text, fontSize: fontSizes.md }}>
                      <span style={{ color: colors.accent }}>{w.type}</span>
                      <span style={{ color: colors.textDim }}> · {w.id}</span>
                    </div>
                    {(w.subscriptions && w.subscriptions.length > 0) && (
                      <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm, marginTop: 4, wordBreak: 'break-all' }}>
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
          <div style={{ color: colors.textDimmed, fontSize: fontSizes.md }}>{t('templates.emptyHint')}</div>
        )}
      </div>
    </div>
  )
}