import { useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { usePolling } from '../hooks/usePolling'
import { CONTROL, fetchTemplates, createTemplate, deleteTemplate } from '../services/api'

// TemplatesView manages the project template set (list, clone, delete). All
// calls go to the control plane on :9527.
export default function TemplatesView() {
  const { colors } = useTheme()
  const [templates, setTemplates] = useState<string[]>([])
  const [newId, setNewId] = useState('')
  const [copyFrom, setCopyFrom] = useState('')
  const [error, setError] = useState('')

  usePolling<string[]>(CONTROL + '/api/templates', 5000, setTemplates, true)

  const create = async () => {
    const id = newId.trim()
    if (!id || !copyFrom) { setError('a name and a source template are required'); return }
    setError('')
    try {
      await createTemplate(id, copyFrom)
      setNewId('')
    } catch (e) {
      setError('failed to create template ' + id)
    }
  }

  const remove = async (name: string) => {
    setError('')
    try {
      await deleteTemplate(name)
    } catch (e) {
      setError('failed to delete template ' + name)
    }
  }

  const source = copyFrom || (templates[0] || '')

  return (
    <div style={{ flex: 1, padding: 24, overflowY: 'auto' }}>
      <h3 style={{ margin: '0 0 16px', color: colors.accent, fontSize: fontSizes.xl }}>Templates</h3>

      {/* Clone a template */}
      <div style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '14px 16px', marginBottom: 20, display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
        <input
          value={newId}
          onChange={(e) => setNewId(e.target.value)}
          placeholder="new template id"
          style={{ flex: 1, minWidth: 160, padding: '6px 10px', fontSize: fontSizes.md, background: colors.bgLight, border: '1px solid ' + colors.border, color: colors.text, outline: 'none' }}
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
          <div key={t} style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '10px 14px', marginBottom: 8, display: 'flex', alignItems: 'center', gap: 10 }}>
            <div style={{ flex: 1, color: colors.text, fontSize: fontSizes.md }}>{t}</div>
            <button
              onClick={() => remove(t)}
              style={{ cursor: 'pointer', background: 'transparent', color: colors.toolFailed, border: '1px solid ' + colors.border, borderRadius: 4, padding: '5px 12px', fontSize: fontSizes.sm }}
            >
              Delete
            </button>
          </div>
        ))
      )}
    </div>
  )
}