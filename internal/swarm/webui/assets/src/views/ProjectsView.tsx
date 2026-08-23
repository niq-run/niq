import { useEffect, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { usePolling } from '../hooks/usePolling'
import { CONTROL, fetchProjects, fetchTemplates, createProject, startProject } from '../services/api'
import type { ProjectInfo } from '../types'

// ProjectsView is the management surface shown in the control plane (and as a
// jump/start hop from a project instance): list projects, start one, and follow
// its WebUI URL. All control-plane calls go to the control server on :9527.
export default function ProjectsView() {
  const { colors } = useTheme()
  const [projects, setProjects] = useState<ProjectInfo[]>([])
  const [templates, setTemplates] = useState<string[]>([])
  const [newName, setNewName] = useState('')
  const [newTemplate, setNewTemplate] = useState('')
  const [starting, setStarting] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [error, setError] = useState('')

  // Refresh the project list periodically so ports / running state stay fresh
  // (projects are launched/unlaunched from this or another WebUI).
  usePolling<ProjectInfo[]>(CONTROL + '/api/projects', 3000, setProjects, true)

  useEffect(() => {
    let active = true
    fetchTemplates()
      .then((list) => { if (active && list.length > 0) setNewTemplate(list[0]); setTemplates(list) })
      .catch(() => {})
    return () => { active = false }
  }, [])

  const create = async () => {
    const name = newName.trim()
    if (!name) { setError('project id is required'); return }
    if (!newTemplate) { setError('a template is required'); return }
    setCreating(true)
    setError('')
    try {
      const res = await createProject(name, newTemplate)
      if (res.webui_port) {
        // Stay in this (dev/control) WebUI, just attach to the new project.
        window.location.href = `?project=${encodeURIComponent(name)}&port=${res.webui_port}`
        return
      }
      fetchProjects().then(setProjects).catch(() => {})
    } catch (e) {
      setError('failed to create project ' + name)
    }
    setCreating(false)
  }

  const start = async (id: string) => {
    setStarting(id)
    setError('')
    try {
      const res = await startProject(id)
      if (res.webui_port) {
        // Stay in this WebUI, just attach to the (re)started project.
        window.location.href = `?project=${encodeURIComponent(id)}&port=${res.webui_port}`
        return
      }
    } catch (e) {
      setError('failed to start project ' + id)
    }
    setStarting(null)
  }

  return (
    <div style={{ flex: 1, padding: 24, overflowY: 'auto' }}>
      <h3 style={{ margin: '0 0 16px', color: colors.accent, fontSize: fontSizes.xl }}>Projects</h3>

      {/* New project: pick a name + template, then create & start. */}
      <div
        style={{
          border: '1px solid ' + colors.border,
          borderRadius: 6,
          padding: '14px 16px',
          marginBottom: 20,
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          flexWrap: 'wrap',
        }}
      >
        <input
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          placeholder="new project id"
          style={{
            flex: 1,
            minWidth: 180,
            padding: '6px 10px',
            fontSize: fontSizes.md,
            background: colors.bgLight,
            border: '1px solid ' + colors.border,
            color: colors.text,
            outline: 'none',
          }}
        />
        {templates.length > 0 && (
          <select
            value={newTemplate}
            onChange={(e) => setNewTemplate(e.target.value)}
            style={{ padding: '6px 8px', fontSize: fontSizes.md, background: colors.bgLight, color: colors.text, border: '1px solid ' + colors.border }}
          >
            {templates.map((t) => <option key={t} value={t}>{t}</option>)}
          </select>
        )}
        <button
          onClick={create}
          disabled={creating}
          style={{
            cursor: creating ? 'default' : 'pointer',
            background: colors.accent,
            color: '#fff',
            border: 'none',
            borderRadius: 4,
            padding: '6px 14px',
            fontSize: fontSizes.sm,
            opacity: creating ? 0.6 : 1,
          }}
        >
          {creating ? 'Creating…' : 'Create & Start'}
        </button>
      </div>

      {error && <div style={{ color: colors.toolFailed, marginBottom: 12, fontSize: fontSizes.sm }}>{error}</div>}

      {projects.length === 0 ? (
        <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>
          No projects yet. Create one from a template, e.g. <code>niq project create demo --template dev</code>.
        </div>
      ) : (
        projects.map((p) => (
          <div
            key={p.id}
            style={{
              border: '1px solid ' + colors.border,
              borderRadius: 6,
              padding: '12px 16px',
              marginBottom: 10,
              display: 'flex',
              alignItems: 'center',
              gap: 12,
            }}
          >
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ color: colors.text, fontSize: fontSizes.md }}>{p.id}</div>
              <div style={{ fontSize: fontSizes.xs, color: colors.textDim }}>
                <span style={{ color: p.ports?.webui ? colors.toolCompleted : colors.textDimmed }}>
                  {p.ports?.webui ? 'running' : 'stopped'}
                </span>
                {' · '}{p.workers?.length ?? 0} workers
                {p.ports?.webui ? ` · webui :${p.ports.webui}` : ''}
                {p.ports?.bus ? ` · bus :${p.ports.bus}` : ''}
              </div>
            </div>
            {p.ports?.webui ? (
              <a
                href={`?project=${encodeURIComponent(p.id)}&port=${p.ports.webui}`}
                style={{ color: colors.accent, fontSize: fontSizes.sm, textDecoration: 'none' }}
              >
                Jump
              </a>
            ) : (
              <button
                onClick={() => start(p.id)}
                disabled={starting === p.id}
                style={{
                  cursor: starting === p.id ? 'default' : 'pointer',
                  background: colors.accent,
                  color: '#fff',
                  border: 'none',
                  borderRadius: 4,
                  padding: '6px 14px',
                  fontSize: fontSizes.sm,
                  opacity: starting === p.id ? 0.6 : 1,
                }}
              >
                {starting === p.id ? 'Starting…' : 'Start'}
              </button>
            )}
          </div>
        ))
      )}
    </div>
  )
}