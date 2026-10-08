import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { usePolling } from '../hooks/usePolling'
import ViewHeader from '../components/ViewHeader'
import { CONTROL, fetchProjects, fetchTemplates, createProject, linkProject, fetchDirs, startProject, stopProject, restartProject, fetchProviders } from '../services/api'
import type { DirEntry } from '../services/api'
import type { ProjectInfo } from '../types'

// ProjectsView is the management surface shown in the control plane (and as a
// jump/start hop from a project instance): list projects, start one, and jump
// into the project's WebUI. Control-plane calls are same-origin (CONTROL), since
// the control plane serves both this page and every /p/<id>/ project page.
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
  // Link-to-open an external project directory: type a path, or browse the
  // control plane's filesystem (server-side directory picker) and pick a folder.
  const [linkPath, setLinkPath] = useState('')
  const [linking, setLinking] = useState(false)
  const [showingPicker, setShowingPicker] = useState(false)
  const [pickerPath, setPickerPath] = useState('')
  const [pickerDirs, setPickerDirs] = useState<DirEntry[] | undefined>(undefined)
  const [pickerLoading, setPickerLoading] = useState(false)
  const [pickerError, setPickerError] = useState('')
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

  // link opens an external directory as a project by contacting the control
  // plane (which symlinks it into the niq root), then refreshes the list so the
  // new project appears without waiting for the poll.
  const link = async () => {
    const p = linkPath.trim()
    if (!p) { setError(t('projects.link.errorPath')); return }
    setLinking(true)
    setError('')
    try {
      await linkProject(p)
      setLinkPath('')
      refresh()
    } catch (e) {
      setError(t('projects.link.errorLink', { path: p }))
    }
    setLinking(false)
  }

  // parentOf strips the last path segment (handles both / and \ separators).
  const parentOf = (p: string | undefined): string | undefined => {
    if (!p) return undefined
    const i = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'))
    if (i <= 0) return p
    return p.slice(0, i)
  }

  const loadDirs = async (path: string | undefined) => {
    setPickerLoading(true)
    setPickerError('')
    try {
      setPickerDirs(await fetchDirs(path))
      setPickerPath(path || '')
    } catch (e: any) {
      setPickerError(String((e as Error)?.message || e))
    }
    setPickerLoading(false)
  }

  const openPicker = () => {
    setShowingPicker(true)
    setPickerDirs(undefined)
    loadDirs(undefined) // start at the niq projects root
  }

  const pickDir = (p: string) => {
    setShowingPicker(false)
    setPickerDirs(undefined)
    setLinkPath(p)
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

      {/* Open an existing project directory: type a path, or browse the control
          plane's filesystem (server-side) and pick a folder. Linking symlinks the
          directory into the niq root so it is scanned while the files stay put. */}
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
          value={linkPath}
          onChange={(e) => setLinkPath(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') link() }}
          placeholder={t('projects.link.placeholder')}
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
        <span
          onClick={openPicker}
          className="btn-hover"
          style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.border, borderRadius: 2, padding: '6px 12px', userSelect: 'none', whiteSpace: 'nowrap' }}
        >
          {t('projects.link.browse')}
        </span>
        <button
          onClick={link}
          disabled={linking}
          style={{
            cursor: linking ? 'default' : 'pointer',
            background: 'transparent',
            color: colors.accent,
            border: '1px solid ' + colors.border,
            borderRadius: 2,
            padding: '6px 14px',
            fontSize: fontSizes.sm,
            opacity: linking ? 0.6 : 1,
          }}
        >
          {linking ? t('projects.linking') : t('projects.link')}
        </button>
      </div>

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
                    href={`/p/${encodeURIComponent(p.id)}/`}
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

      {/* Server-side directory picker: browse the control plane's filesystem and
          pick a folder to open as a project. Works from localhost and remote alike
          because the listing happens on the server (browsers can't expose a real
          absolute path). */}
      {showingPicker && (
        <div style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 500 }}>
          <div style={{ width: 560, maxWidth: '92vw', background: colors.bg, border: '1px solid ' + colors.border, borderRadius: 8, boxShadow: '0 8px 30px rgba(0,0,0,0.3)' }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '14px 16px', borderBottom: '1px solid ' + colors.border }}>
              <strong style={{ color: colors.text, fontSize: fontSizes.md }}>{t('projects.link.pickerTitle')}</strong>
              <span onClick={() => setShowingPicker(false)} className="btn-hover" style={{ cursor: 'pointer', marginLeft: 'auto', color: colors.textDim, fontSize: fontSizes.md, border: '1px solid ' + colors.border, borderRadius: 4, padding: '0 8px', lineHeight: '20px', userSelect: 'none' }}>✕</span>
            </div>
            {pickerError && <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm, padding: '10px 16px' }}>{pickerError}</div>}
            <div style={{ padding: '12px 16px', display: 'flex', alignItems: 'center', gap: 8 }}>
              <span onClick={() => loadDirs(parentOf(pickerPath))} className="btn-hover" style={{ cursor: 'pointer', color: colors.accent, fontSize: fontSizes.sm, border: '1px solid ' + colors.border, borderRadius: 2, padding: '4px 10px', userSelect: 'none', whiteSpace: 'nowrap' }}>{t('projects.link.up')}</span>
              <span style={{ color: colors.textDim, fontSize: fontSizes.sm, fontFamily: 'monospace', wordBreak: 'break-all', minWidth: 0 }}>{pickerPath || t('projects.link.root')}</span>
            </div>
            <div style={{ maxHeight: 320, overflowY: 'auto', padding: '4px 8px' }}>
              {pickerLoading && <div style={{ color: colors.textDim, fontSize: fontSizes.sm, padding: 12 }}>{t('projects.link.loading')}</div>}
              {!pickerLoading && !pickerError && (pickerDirs || []).length === 0 && (
                <div style={{ color: colors.textDim, fontSize: fontSizes.sm, padding: 12 }}>{t('projects.link.noDirs')}</div>
              )}
              {(pickerDirs || []).map((d) => (
                <div key={d.path} className="btn-hover" style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '7px 10px', borderRadius: 4, cursor: 'pointer' }} onClick={() => loadDirs(d.path)}>
                  <span style={{ color: colors.textDim }}>📁</span>
                  <span style={{ color: colors.text, fontSize: fontSizes.md }}>{d.name}</span>
                  <span onClick={(e) => { e.stopPropagation(); pickDir(d.path) }} className="btn-hover" style={{ marginLeft: 'auto', color: colors.accent, fontSize: fontSizes.sm, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '2px 10px', userSelect: 'none', whiteSpace: 'nowrap' }}>{t('projects.link.select')}</span>
                </div>
              ))}
            </div>
            <div style={{ padding: '12px 16px', borderTop: '1px solid ' + colors.border, display: 'flex', gap: 8, alignItems: 'center' }}>
              {pickerPath && (
                <span onClick={() => pickDir(pickerPath)} className="btn-hover" style={{ cursor: 'pointer', color: colors.accent, fontSize: fontSizes.sm, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '5px 12px', userSelect: 'none' }}>{t('projects.link.selectHere')}</span>
              )}
              <span onClick={() => setShowingPicker(false)} className="btn-hover" style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, border: '1px solid ' + colors.border, borderRadius: 2, padding: '5px 12px', userSelect: 'none', marginLeft: 'auto' }}>{t('projects.link.cancel')}</span>
            </div>
          </div>
        </div>
      )}
      </div>
    </div>
  )
}