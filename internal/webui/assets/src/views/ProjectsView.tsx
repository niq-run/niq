import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { usePolling } from '../hooks/usePolling'
import ViewHeader from '../components/ViewHeader'
import { CONTROL, fetchProjects, fetchTemplates, createProject, startProject, stopProject, restartProject, fetchProviders } from '../services/api'
import type { ProjectInfo } from '../types'

// ProjectsView is the management surface shown in the control plane (and as a
// jump/start hop from a project instance): list projects, start one, and follow
// its WebUI URL. All control-plane calls go to the control server on :9527.
interface ProjectsViewProps {
  onGoToProviders?: () => void
}

export default function ProjectsView({ onGoToProviders }: ProjectsViewProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [projects, setProjects] = useState<ProjectInfo[]>([])
  const [templates, setTemplates] = useState<string[]>([])
  const [newName, setNewName] = useState('')
  const [newTemplate, setNewTemplate] = useState('')
  // The project currently being started or restarted, with which op — drives the
  // loading spinner. It is cleared once the control plane reports the project
  // ready (the backend blocks until the WebUI port is actually listening).
  const [busy, setBusy] = useState<{ id: string; op: 'start' | 'restart' | 'stop' } | null>(null)
  const [creating, setCreating] = useState(false)
  const [provNotice, setProvNotice] = useState(false)
  // 'create anyway' consumes this: the next create skips the provider check.
  const bypassProvCheck = useRef(false)
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
    if (!name) { setError(t('projects.error.idRequired')); return }
    if (!newTemplate) { setError(t('projects.error.templateRequired')); return }
    setCreating(true)
    setError('')
    try {
      // Gate on a usable provider (mirrors provider.Configured): a project
      // without one starts up mute. The notice offers a jump to the
      // providers panel; 'create anyway' bypasses the check once.
      if (!provNotice && !bypassProvCheck.current) {
        const cfg = await fetchProviders()
        const usable = (cfg.providers || []).some((p: any) => (p.api_key || '') !== '')
        if (!usable) {
          setProvNotice(true)
          setCreating(false)
          return
        }
      }
      setProvNotice(false)
      bypassProvCheck.current = false
      await createProject(name, newTemplate)
      setNewName('')
      refresh()
    } catch (e) {
      setError(t('projects.error.create', { name }))
    }
    setCreating(false)
  }

  const start = async (id: string) => {
    setBusy({ id, op: 'start' })
    setError('')
    try {
      await startProject(id)
      refresh()
    } catch (e) {
      setError(t('projects.error.start', { id }))
    }
    setBusy(null)
  }

  const restart = async (id: string) => {
    setBusy({ id, op: 'restart' })
    setError('')
    try {
      await restartProject(id)
      refresh()
    } catch (e) {
      setError(t('projects.error.restart', { id }))
    }
    setBusy(null)
  }

  // Force an immediate project-list refresh (the 3s poll would otherwise leave a
  // stale 'stopped' state up to an interval, briefly flashing the Start button).
  const refresh = async () => {
    try { setProjects(await fetchProjects()) } catch {}
  }

  const stop = async (id: string) => {
    setBusy({ id, op: 'stop' })
    setError('')
    try {
      await stopProject(id)
      // Refresh immediately so 'running' flips without waiting for the poll.
      refresh()
    } catch (e) {
      setError(t('projects.error.stop', { id }))
    }
    setBusy(null)
  }

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      <ViewHeader title={t('projects.title')} />
      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: 24 }}>

      {/* Provider gate notice: shown when a create was attempted without a
          usable provider (api_key empty everywhere). */}
      {provNotice && (
        <div style={{ border: '1px solid ' + colors.accent, borderRadius: 6, padding: '10px 14px', marginBottom: 16, display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
          <span style={{ color: colors.text, fontSize: fontSizes.sm, flex: 1, minWidth: 200 }}>{t('projects.noProvider')}</span>
          <span
            onClick={() => onGoToProviders?.()}
            className="btn-hover"
            style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '3px 12px', userSelect: 'none' }}
          >
            {t('projects.goConfigure')}
          </span>
          <span
            onClick={() => { bypassProvCheck.current = true; setProvNotice(false); setError('') }}
            className="btn-hover"
            style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 2, padding: '3px 12px', userSelect: 'none' }}
          >
            {t('projects.createAnyway')}
          </span>
        </div>
      )}

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
          placeholder={t('projects.newId.placeholder')}
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
          {creating ? t('projects.creating') : t('projects.create')}
        </button>
      </div>

      {error && <div style={{ color: colors.toolFailed, marginBottom: 12, fontSize: fontSizes.sm }}>{error}</div>}

      {projects.length === 0 ? (
        <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>
          {t('projects.empty', { cmd: 'niq project create demo --template default' })}
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
                <span style={{ color: p.running ? colors.toolCompleted : colors.textDimmed }}>
                  {p.running ? t('projects.running') : t('projects.stopped')}
                </span>
                {' · '}{t('projects.workerCount', { n: p.workers?.length ?? 0 })}
                {p.ports?.webui ? ` · webui :${p.ports.webui}` : ''}
                {p.ports?.bus ? ` · bus :${p.ports.bus}` : ''}
              </div>
            </div>
            {busy?.id === p.id ? (
              <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: fontSizes.sm, color: colors.textDim }}>
                <span className="niq-spinner" style={{ width: 13, height: 13, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />
                {busy.op === 'stop' ? t('projects.stopping') : busy.op === 'restart' ? t('projects.restarting') : t('projects.starting')}
              </span>
            ) : p.running ? (
              <>
                {p.ports?.webui && (
                  <a
                    href={`?project=${encodeURIComponent(p.id)}&port=${p.ports.webui}`}
                    target={'_blank'}
                    rel="noopener noreferrer"
                    style={{ color: colors.accent, fontSize: fontSizes.sm, textDecoration: 'none' }}
                  >
                    {t('projects.jump')}
                  </a>
                )}
                <button
                  onClick={() => restart(p.id)}
                  style={{
                    cursor: 'pointer',
                    background: 'transparent',
                    color: colors.accent,
                    border: '1px solid ' + colors.border,
                    borderRadius: 2,
                    padding: '6px 10px',
                    fontSize: fontSizes.sm,
                  }}
                >
                  {t('projects.restart')}
                </button>
                <button
                  onClick={() => stop(p.id)}
                  style={{
                    cursor: 'pointer',
                    background: 'transparent',
                    color: colors.toolFailed,
                    border: '1px solid ' + colors.border,
                    borderRadius: 2,
                    padding: '6px 10px',
                    fontSize: fontSizes.sm,
                  }}
                >
                  {t('projects.stop')}
                </button>
              </>
            ) : (
              <button
                onClick={() => start(p.id)}
                style={{
                  cursor: 'pointer',
                  background: colors.accent,
                  color: '#fff',
                  border: 'none',
                  borderRadius: 2,
                  padding: '6px 14px',
                  fontSize: fontSizes.sm,
                }}
              >
                {t('projects.start')}
              </button>
            )}
          </div>
        ))
      )}
      </div>
    </div>
  )
}