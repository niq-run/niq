import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { createWorker } from '../services/api'
import { type CreateWorkerResult } from '../types'

interface CreateWorkerDialogProps {
  open: boolean
  onClose: () => void
  onCreated: (r: CreateWorkerResult) => void
}

// Managed worker types offered by the form. host/hiw are infrastructure the
// assembly owns — creating them from the UI is not a thing.
const MANAGED_TYPES = ['reason', 'workspace', 'timer', 'program'] as const

// CreateWorkerDialog is the workers-view "create worker" form: it persists a
// declaration into project.json (plus workers/<id>/config.json for a managed
// worker) and launches it — external processes directly, managed workers via
// the host worker's spawn event. A centered overlay on both desktop and
// mobile: the trigger is a header pill (no anchor for a dropdown) and the form
// is far larger than a picker list.
export default function CreateWorkerDialog({ open, onClose, onCreated }: CreateWorkerDialogProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [mode, setMode] = useState<'managed' | 'external'>('managed')
  const [type, setType] = useState('reason')
  const [extType, setExtType] = useState('')
  const [id, setId] = useState('')
  const [instruction, setInstruction] = useState('')
  const [provider, setProvider] = useState('')
  const [model, setModel] = useState('')
  const [rootDir, setRootDir] = useState('')
  const [command, setCommand] = useState('')
  const [cwd, setCwd] = useState('')
  const [envText, setEnvText] = useState('')
  const [subscriptions, setSubscriptions] = useState('')
  const [publish, setPublish] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  if (!open) return null

  const isExternal = mode === 'external'
  const effType = isExternal ? extType.trim() : type

  const input: React.CSSProperties = {
    width: '100%',
    boxSizing: 'border-box',
    background: colors.bg,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    padding: '5px 8px',
    color: colors.text,
    fontSize: fontSizes.sm,
    outline: 'none',
  }
  const label: React.CSSProperties = {
    display: 'block',
    fontSize: fontSizes.xs,
    color: colors.textDimmed,
    marginBottom: 4,
  }
  const field: React.CSSProperties = { marginBottom: 10 }
  const pill = (active: boolean): React.CSSProperties => ({
    cursor: 'pointer',
    userSelect: 'none',
    border: '1px solid ' + (active ? colors.accent : colors.border),
    borderRadius: 4,
    padding: '3px 12px',
    fontSize: fontSizes.sm,
    color: active ? colors.accent : colors.textDim,
    background: active ? colors.bgChip : undefined,
  })

  // typesFromList splits a comma-separated event-type list into bare specs.
  const typesFromList = (s: string) =>
    s.split(',').map(x => x.trim()).filter(Boolean)

  const submit = async () => {
    if (busy) return
    setNote('')
    if (!effType) { setNote(t('workers.create.errType')); return }
    if (!/^[A-Za-z0-9._-]+$/.test(id)) { setNote(t('workers.create.errId')); return }
    if (isExternal && !command.trim()) { setNote(t('workers.create.errCommand')); return }

    const env: Record<string, string> = {}
    for (const line of envText.split('\n')) {
      const i = line.indexOf('=')
      if (i > 0) env[line.slice(0, i).trim()] = line.slice(i + 1).trim()
    }
    const body: Record<string, unknown> = {
      type: effType,
      id: id.trim(),
      managed: !isExternal,
    }
    if (!isExternal) {
      if (effType === 'reason') {
        if (instruction.trim()) body.instruction = instruction.trim()
        if (provider.trim()) body.provider = provider.trim()
        if (model.trim()) body.model = model.trim()
      }
      if ((effType === 'workspace' || effType === 'program') && rootDir.trim()) {
        body.root_dir = rootDir.trim()
      }
    } else {
      body.command = command.trim().split(/\s+/)
      if (cwd.trim()) body.cwd = cwd.trim()
      if (Object.keys(env).length > 0) body.env = env
    }
    const subs = typesFromList(subscriptions)
    const pubs = typesFromList(publish)
    if (subs.length > 0) body.subscriptions = subs.map(x => ({ type: x }))
    if (pubs.length > 0) body.publish = pubs.map(x => ({ type: x }))

    setBusy(true)
    try {
      const r = await createWorker(body)
      onCreated(r)
    } catch (e) {
      setNote((e as Error)?.message || 'create failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {/* Backdrop: tapping it closes the dialog. */}
      <div
        onClick={onClose}
        style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', zIndex: 90 }}
      />
      <div
        onClick={e => e.stopPropagation()}
        style={{
          position: 'fixed',
          top: '50%',
          left: '50%',
          transform: 'translate(-50%, -50%)',
          width: 'min(460px, calc(100vw - 32px))',
          maxHeight: '80vh',
          overflowY: 'auto',
          background: colors.bgLight,
          border: '1px solid ' + colors.border,
          borderRadius: 6,
          boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
          zIndex: 91,
          padding: '14px 16px',
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', marginBottom: 10 }}>
          <span style={{ color: colors.text, fontSize: fontSizes.md }}>{t('workers.create.title')}</span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{ cursor: 'pointer', marginLeft: 'auto', border: '1px solid ' + colors.border, borderRadius: 4, padding: '0 8px', color: colors.textDim, fontSize: fontSizes.sm, lineHeight: '20px', userSelect: 'none' }}
          >
            {'\u2715'}
          </span>
        </div>

        {/* Mode: host-managed in-process worker vs external OS process. */}
        <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
          <span onClick={() => { setMode('managed'); setNote('') }} style={pill(mode === 'managed')}>
            {t('workers.create.managed')}
          </span>
          <span onClick={() => { setMode('external'); setNote('') }} style={pill(mode === 'external')}>
            {t('workers.create.external')}
          </span>
        </div>
        <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, lineHeight: 1.5, marginBottom: 12 }}>
          {isExternal ? t('workers.create.externalHint') : t('workers.create.managedHint')}
        </div>

        <div style={field}>
          <label style={label}>{t('workers.create.type')}</label>
          {isExternal ? (
            <input style={input} value={extType} onChange={e => setExtType(e.target.value)} placeholder={t('workers.create.typePlaceholder')} />
          ) : (
            <select style={input} value={type} onChange={e => setType(e.target.value)}>
              {MANAGED_TYPES.map(x => <option key={x} value={x}>{x}</option>)}
            </select>
          )}
        </div>
        <div style={field}>
          <label style={label}>{t('workers.create.id')}</label>
          <input style={input} value={id} onChange={e => setId(e.target.value)} placeholder="my-worker" />
        </div>

        {!isExternal && effType === 'reason' && (
          <>
            <div style={field}>
              <label style={label}>{t('workers.create.instruction')}</label>
              <textarea style={{ ...input, resize: 'vertical', minHeight: 64 }} value={instruction} onChange={e => setInstruction(e.target.value)} />
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <div style={{ ...field, flex: 1 }}>
                <label style={label}>{t('workers.create.provider')}</label>
                <input style={input} value={provider} onChange={e => setProvider(e.target.value)} />
              </div>
              <div style={{ ...field, flex: 1 }}>
                <label style={label}>{t('workers.create.model')}</label>
                <input style={input} value={model} onChange={e => setModel(e.target.value)} />
              </div>
            </div>
          </>
        )}
        {!isExternal && (effType === 'workspace' || effType === 'program') && (
          <div style={field}>
            <label style={label}>{t('workers.create.rootDir')}</label>
            <input style={input} value={rootDir} onChange={e => setRootDir(e.target.value)} />
          </div>
        )}

        {isExternal && (
          <>
            <div style={field}>
              <label style={label}>{t('workers.create.command')}</label>
              <input style={input} value={command} onChange={e => setCommand(e.target.value)} placeholder="npx -y some-worker" />
            </div>
            <div style={field}>
              <label style={label}>{t('workers.create.cwd')}</label>
              <input style={input} value={cwd} onChange={e => setCwd(e.target.value)} />
            </div>
            <div style={field}>
              <label style={label}>{t('workers.create.env')}</label>
              <textarea style={{ ...input, resize: 'vertical', minHeight: 48 }} value={envText} onChange={e => setEnvText(e.target.value)} />
            </div>
          </>
        )}

        <div style={field}>
          <label style={label}>{t('workers.create.subscriptions')}</label>
          <input style={input} value={subscriptions} onChange={e => setSubscriptions(e.target.value)} placeholder="worker.input, timer.timeout" />
        </div>
        <div style={field}>
          <label style={label}>{t('workers.create.publish')}</label>
          <input style={input} value={publish} onChange={e => setPublish(e.target.value)} placeholder="request.*" />
        </div>

        {note && (
          <div style={{ fontSize: fontSizes.sm, color: colors.toolFailed, marginBottom: 10, lineHeight: 1.5, wordBreak: 'break-all' }}>{note}</div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <span
            onClick={submit}
            className="btn-hover"
            style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, border: '1px solid ' + colors.accent, borderRadius: 4, padding: '4px 14px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {busy ? t('workers.create.saving') : t('workers.create.submit')}
          </span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{ cursor: 'pointer', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 14px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {t('wd.cancel')}
          </span>
        </div>
      </div>
    </>
  )
}
