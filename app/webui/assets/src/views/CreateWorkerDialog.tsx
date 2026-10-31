import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { createWorker } from '../services/api'
import { isReasonFamilyType, type CreateWorkerResult, type WorkerInfo } from '../types'
import PermissionPanel from '../components/PermissionPanel'
import type { PublishRow, SubscribeRow } from '../components/AllowEditors'
import { defaultAllow, samePublish, sameSubscribe, type AllowDefaults, type WorkerCreateMode } from '../allowDefaults'

interface CreateWorkerDialogProps {
  open: boolean
  onClose: () => void
  onCreated: (r: CreateWorkerResult) => void
  // busHost/busPort describe the event bus an external/remote worker connects
  // to, used to render the connect configuration for remote workers.
  busHost?: string
  busPort?: number
  // When set, the dialog is a BUILDER: submit hands the form's body object to
  // onBuild instead of creating a live worker (the template drawer uses this
  // to append a worker entry into the template's JSON). The form and its
  // validation are exactly the same — only the destination differs.
  onBuild?: (body: Record<string, unknown>) => void
  // Live worker list for the permission module's "pick a worker's published
  // events" helper. Optional: the module fetches the list itself when absent.
  workers?: WorkerInfo[]
}

// Managed worker types offered by the form. host/hiw are infrastructure the
// assembly owns — creating them from the UI is not a thing. timer, directory
// and history are built-in singletons (one per project), so a second one is
// never wanted. niw is a bridge worker that dials another niq's bus.
const MANAGED_TYPES = ['reason', 'coding', 'workspace', 'program', 'niw', 'mcp'] as const

// The form opens on a managed reason worker; its permission drafts start from
// that type's backend defaults (see allowDefaults.ts).
const INITIAL_MODE: WorkerCreateMode = 'managed'
const INITIAL_TYPE = 'reason'
const INITIAL_ALLOW = defaultAllow(INITIAL_MODE, INITIAL_TYPE)

// parseNiwConfig turns the far-side bus's copyable worker config (KEY=value
// lines, as rendered by that bus for a created remote worker) into a map.
const parseNiwConfig = (s: string): Record<string, string> => {
  const out: Record<string, string> = {}
  for (const line of s.split('\n')) {
    const i = line.indexOf('=')
    const k = line.slice(0, i).trim()
    const v = line.slice(i + 1).trim()
    if (i > 0 && k) out[k] = v
  }
  return out
}

// CreateWorkerDialog is the workers-view "create worker" form: it persists a
// declaration into project.json (plus workers/<id>/config.json for a managed
// worker) and launches it — external processes directly, managed workers via
// the host worker's spawn event. A centered overlay on both desktop and
// mobile: the trigger is a header pill (no anchor for a dropdown) and the form
// is far larger than a picker list.
export default function CreateWorkerDialog({ open, onClose, onCreated, busHost, busPort, onBuild, workers }: CreateWorkerDialogProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [mode, setMode] = useState<'managed' | 'external' | 'remote'>(INITIAL_MODE)
  const [type, setType] = useState(INITIAL_TYPE)
  const [extType, setExtType] = useState('')
  const [id, setId] = useState('')
  const [instruction, setInstruction] = useState('')
  const [readme, setReadme] = useState('')
  const [provider, setProvider] = useState('')
  const [model, setModel] = useState('')
  // Spawn seeding: goal rides the system prompt (survives compaction), brief
  // lands as the transcript's first message (working material).
  const [goal, setGoal] = useState('')
  const [brief, setBrief] = useState('')
  // Coding-only: the tool-list policy. "ptc" keeps only the program +
  // directory workers' tools and drives work through script programs.
  const [toolMode, setToolMode] = useState('normal')
  const [rootDir, setRootDir] = useState('')
  const [command, setCommand] = useState('')
  const [cwd, setCwd] = useState('')
  // Environment variables for an external process: an editable key/value list
  // (not a KEY=value text blob), so a value containing '=' or newlines can be
  // entered without ambiguity.
  const [envRows, setEnvRows] = useState<{ key: string; value: string }[]>([])
  // Allow-list drafts: the same row shapes the worker detail's permission tab
  // edits, rendered by the shared 订阅发布管理 module. They open on the current
  // type's backend defaults, so what the form shows is what the worker gets.
  const [subscriptions, setSubscriptions] = useState<SubscribeRow[]>(INITIAL_ALLOW.subscribe)
  const [publish, setPublish] = useState<PublishRow[]>(INITIAL_ALLOW.publish)
  // MCP (managed worker): expose mode + the remote servers as a JSON list.
  const [mcpExpose, setMcpExpose] = useState('agentic')
  const [mcpFixed, setMcpFixed] = useState(false)
  const [mcpServersText, setMcpServersText] = useState('')
  const [mcpAllowedTools, setMcpAllowedTools] = useState('')
  const [tokenTtl, setTokenTtl] = useState('')
  const [niwConfig, setNiwConfig] = useState('')
  const [niwRecipient, setNiwRecipient] = useState('')
  const [workerLabel, setWorkerLabel] = useState('')
  const [tags, setTags] = useState('')
  const [description, setDescription] = useState('')
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [result, setResult] = useState<CreateWorkerResult | null>(null)
  const [copied, setCopied] = useState(false)

  // The permission drafts follow the selected worker kind/type: switching type
  // re-seeds them from that type's defaults, but only while the rows are still
  // untouched — a hand-edited allow list is never silently thrown away.
  const effectiveType = mode === 'managed' ? type : extType.trim()
  const allowKey = mode + ':' + effectiveType
  const allowRef = useRef<{ key: string; value: AllowDefaults }>({
    key: INITIAL_MODE + ':' + INITIAL_TYPE,
    value: INITIAL_ALLOW,
  })
  useEffect(() => {
    if (allowRef.current.key === allowKey) return
    const next = defaultAllow(mode, effectiveType)
    const prev = allowRef.current.value
    setPublish((cur) => (samePublish(cur, prev.publish) ? next.publish : cur))
    setSubscriptions((cur) => (sameSubscribe(cur, prev.subscribe) ? next.subscribe : cur))
    allowRef.current = { key: allowKey, value: next }
  }, [allowKey, mode, effectiveType])

  if (!open) return null

  const isExternal = mode === 'external'
  const isRemote = mode === 'remote'
  const isManaged = mode === 'managed'
  const effType = effectiveType

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
    borderRadius: 2,
    padding: '3px 12px',
    fontSize: fontSizes.sm,
    color: active ? colors.accent : colors.textDim,
    background: active ? colors.bgChip : undefined,
  })

  // buildBody assembles the worker declaration from the form fields — the
  // shared core of both destinations (create a live worker / append to a
  // template's workers). Returns null after recording a validation note.
  const buildBody = (): Record<string, unknown> | null => {
    if (!effType) { setNote(t('workers.create.errType')); return null }
    if (!/^[A-Za-z0-9._-]+$/.test(id)) { setNote(t('workers.create.errId')); return null }
    if (isExternal && !command.trim()) { setNote(t('workers.create.errCommand')); return null }

    const env: Record<string, string> = {}
    for (const row of envRows) {
      const k = row.key.trim()
      if (k) env[k] = row.value
    }
    const body: Record<string, unknown> = {
      type: effType,
      id: id.trim(),
      managed: isManaged,
    }
    // A remote (third-party) worker: host-managed=false, remote=true, and no
    // local command — the worker connects to the bus on its own.
    if (isRemote) body.remote = true
    // Display metadata: label (the name shown instead of the id), slash-path
    // tags (grouping) and a purpose note. Applies to every worker kind, managed
    // or external; carried in the declaration.
    if (workerLabel.trim()) body.label = workerLabel.trim()
    const tagList = tags.split(',').map(x => x.trim()).filter(Boolean)
    if (tagList.length > 0) body.tags = tagList
    if (description.trim()) body.description = description.trim()
    if (isManaged) {
      if (isReasonFamilyType(effType)) {
        if (instruction.trim()) body.instruction = instruction.trim()
        if (provider.trim()) body.provider = provider.trim()
        if (model.trim()) body.model = model.trim()
        // Spawn seeding has no typed declaration field: it is a param
        // (context-builder: goal → system prompt, brief → first message).
        const params: Record<string, unknown> = {}
        if (goal.trim()) params.goal = goal.trim()
        if (brief.trim()) params.brief = brief.trim()
        // Self-description (worker.readme). A param, not display metadata: the
        // worker can revise it at runtime and other workers read it by recall.
        if (readme.trim()) params.readme = readme.trim()
        // Coding's only knob: the tool-list policy. Which peers PTC keeps is
        // part of the PTC contract (program for invocation, directory for
        // discovery), not configuration.
        if (effType === 'coding') params.tool_mode = toolMode
        if (Object.keys(params).length > 0) body.params = params
      }
      if ((effType === 'workspace' || effType === 'program') && rootDir.trim()) {
        // WorkerConfig takes a mounts array (first entry = primary mount).
        // More mounts can be added at runtime via the workspace's mount.add.
        body.mounts = [rootDir.trim()]
      }
      if (effType === 'mcp') {
        // MCP worker: params.mcp carries the expose mode + remote servers.
        // headers may reference env as ${VAR}; plaintext is accepted for now.
        let servers: unknown[] = []
        if (mcpServersText.trim()) {
          try {
            const parsed = JSON.parse(mcpServersText)
            if (!Array.isArray(parsed)) { setNote(t('workers.create.mcpJsonErr')); return null }
            for (const s of parsed) {
              if (!s || typeof s !== 'object' || !(s as any).name || !(s as any).url) {
                setNote(t('workers.create.mcpJsonErr')); return null
              }
            }
            servers = parsed
          } catch {
            setNote(t('workers.create.mcpJsonErr')); return null
          }
        }
        const allowedTools = mcpAllowedTools.split(',').map(x => x.trim()).filter(Boolean)
        body.params = {
          expose: mcpExpose,
          fixed: mcpFixed,
          servers,
          ...(allowedTools.length > 0 ? { allowed_tools: allowedTools } : {}),
        }
      }
    } else if (isExternal) {
      body.command = command.trim().split(/\s+/)
      if (cwd.trim()) body.cwd = cwd.trim()
      if (Object.keys(env).length > 0) body.env = env
    }
    if (isManaged && effType === 'niw') {
      // NIW is a MANAGED bridge worker: map the pasted far-side config onto
      // the NIW construction params (remote_url / remote_worker_id /
      // remote_credential) plus the optional local recipient seed. This is
      // intentionally scoped to managed mode — a THIRD-PARTY worker typing
      // "niw" (or "remote-niw") is a remote peer, not a local bridge, and
      // must not hit the local-niw parameter requirements.
      const c = parseNiwConfig(niwConfig)
      const params: Record<string, unknown> = {}
      if (c.NIQ_BUS_URL) params.remote_url = c.NIQ_BUS_URL
      if (c.NIQ_WORKER_ID) params.remote_worker_id = c.NIQ_WORKER_ID
      if (c.NIQ_WORKER_CREDENTIAL) params.remote_credential = c.NIQ_WORKER_CREDENTIAL
      if (niwRecipient.trim()) params.recipient = niwRecipient.trim()
      if (!params.remote_url || !params.remote_worker_id) {
        setNote(t('workers.create.niwErr'))
        return null
      }
      body.params = params
    }
    // Optional token lifetime override for workers whose credential WE issue
    // (external second-party and remote third-party workers).
    if ((isExternal || isRemote) && tokenTtl.trim()) {
      const d = Number(tokenTtl.trim())
      if (Number.isFinite(d) && d > 0) body.token_ttl_seconds = Math.round(d * 86400)
    }
    const subs = subscriptions.filter((r) => r.type.trim() !== '')
    const pubs = publish.filter((r) => r.type.trim() !== '')
    if (subs.length > 0) {
      // An empty source is the legacy spelling of "any source"; write the
      // canonical wildcard so the declaration matches what the backend stores.
      body.subscriptions = subs.map((r) => ({ type: r.type.trim(), source: r.source.trim() || '*' }))
    }
    if (pubs.length > 0) {
      body.publish = pubs.map((r) => ({ type: r.type.trim(), target: r.target.trim() || '*' }))
    }
    return body
  }

  const submit = async () => {
    if (busy) return
    setNote('')
    const body = buildBody()
    if (!body) return

    // Builder mode (template drawer): hand the body over, nothing is launched.
    if (onBuild) {
      onBuild(body)
      return
    }

    setBusy(true)
    try {
      const r = await createWorker(body)
      onCreated(r)
      // Remote workers have no live lifecycle to direct the user to; instead
      // surface the connect configuration (bus URL + id + credential) so the
      // third party can copy it and connect on its own.
      if (r.remote && r.credential) {
        setResult(r)
      } else {
        onClose()
      }
    } catch (e) {
      setNote((e as Error)?.message || 'create failed')
    } finally {
      setBusy(false)
    }
  }

  // connectConfig renders the env-var block a remote worker consumes to reach
  // the bus on its own (the same convention NIQ_BUS_URL / NIQ_WORKER_ID /
  // NIQ_WORKER_CREDENTIAL carry for supervisor-launched workers).
  const remoteConfig = (): string => {
    const busBase = `${window.location.hostname || 'localhost'}:${busPort ?? 'PORT'}`
    return [
      `NIQ_BUS_URL=http://${busBase}`,
      `NIQ_WORKER_ID=${result?.id ?? ''}`,
      `NIQ_WORKER_CREDENTIAL=${result?.credential ?? ''}`,
      `# type=${result?.type ?? ''}`,
    ].join('\n')
  }

  const copyRemoteConfig = async () => {
    try {
      await navigator.clipboard.writeText(remoteConfig())
      setCopied(true)
      setNote('')
    } catch {
      setNote(t('workers.create.copyFailed'))
    }
  }

  const copyCredential = async () => {
    try {
      await navigator.clipboard.writeText(result?.credential ?? '')
      setCopied(true)
      setNote('')
    } catch {
      setNote(t('workers.create.copyFailed'))
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
          display: 'flex',
          flexDirection: 'column',
          overflow: 'hidden',
          background: colors.bgLight,
          border: '1px solid ' + colors.border,
          borderRadius: 6,
          boxShadow: '0 4px 12px rgba(0,0,0,0.15)',
          zIndex: 91,
        }}
      >
        <div style={{ display: 'flex', alignItems: 'center', padding: '14px 16px 10px' }}>
          <span style={{ color: colors.text, fontSize: fontSizes.md }}>{onBuild ? t('templates.addWorker.title') : t('workers.create.title')}</span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{ cursor: 'pointer', marginLeft: 'auto', border: '1px solid ' + colors.border, borderRadius: 2, padding: '0 8px', color: colors.textDim, fontSize: fontSizes.sm, lineHeight: '20px', userSelect: 'none' }}
          >
            {'\u2715'}
          </span>
        </div>

        {/* Scrollable body, between the pinned header and action row. */}
        <div style={{ overflowY: 'auto', flex: 1, minHeight: 0, padding: '0 16px 14px' }}>
        {/* Mode + form: once a remote worker is created, only its copyable
            connect configuration remains — the used-up form is hidden. */}
        {!result && (<>
        <div style={{ display: 'flex', gap: 8, marginBottom: 8, flexWrap: 'wrap' }}>
          <span onClick={() => { setMode('managed'); setNote(''); setResult(null) }} style={pill(mode === 'managed')}>
            {t('workers.create.managed')}
          </span>
          <span onClick={() => { setMode('external'); setNote(''); setResult(null) }} style={pill(mode === 'external')}>
            {t('workers.create.external')}
          </span>
          {!onBuild && (
            <span onClick={() => { setMode('remote'); setNote(''); setResult(null) }} style={pill(mode === 'remote')}>
              {t('workers.create.remote')}
            </span>
          )}
        </div>
        <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, lineHeight: 1.5, marginBottom: 12 }}>
          {isRemote ? t('workers.create.remoteHint') : isExternal ? t('workers.create.externalHint') : t('workers.create.managedHint')}
        </div>

        <div style={field}>
          <label style={label}>{t('workers.create.type')}</label>
          {isExternal || isRemote ? (
            <input style={input} value={extType} onChange={e => { setExtType(e.target.value); setNote('') }} placeholder={t('workers.create.typePlaceholder')} />
          ) : (
            <select style={input} value={type} onChange={e => setType(e.target.value)}>
              {MANAGED_TYPES.map(x => <option key={x} value={x}>{x}</option>)}
            </select>
          )}
          {isRemote && (
            <div style={{ display: 'flex', gap: 6, marginTop: 6, flexWrap: 'wrap' }}>
              <span
                onClick={() => { setExtType('remote-niw'); setNote('') }}
                className="btn-hover"
                style={{ cursor: 'pointer', border: '1px solid ' + (extType === 'remote-niw' ? colors.accent : colors.border), borderRadius: 3, padding: '2px 10px', fontSize: fontSizes.xs, color: (extType === 'remote-niw' ? colors.accent : colors.textDim), userSelect: 'none' }}
              >
                remote-niw
              </span>
            </div>
          )}
          {isRemote && (
            <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 6, lineHeight: 1.5 }}>
              {t('workers.create.remoteNiwHint')}
            </div>
          )}
        </div>
        <div style={field}>
          <label style={label}>{t('workers.create.id')}</label>
          <input style={input} value={id} onChange={e => setId(e.target.value)} placeholder="my-worker" />
        </div>

        {/* Display metadata: the label is the name shown in place of the id,
            slash-path tags group the worker in the target picker, and the
            description says what it is for. Applies to all kinds. */}
        <div style={field}>
          <label style={label}>{t('workers.create.label')}</label>
          <input style={input} value={workerLabel} onChange={e => setWorkerLabel(e.target.value)} placeholder={t('workers.create.label.placeholder')} />
        </div>
        <div style={field}>
          <label style={label}>{t('workers.create.tags')}</label>
          <input style={input} value={tags} onChange={e => setTags(e.target.value)} placeholder="ops/backup, service/analytics" />
          <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
            {t('workers.create.tags.hint')}
          </div>
        </div>
        <div style={field}>
          <label style={label}>{t('workers.create.description')}</label>
          <input style={input} value={description} onChange={e => setDescription(e.target.value)} placeholder={t('workers.create.description.placeholder')} />
        </div>

        {isManaged && isReasonFamilyType(effType) && (
          <>
            <div style={field}>
              <label style={label}>{t('workers.create.instruction')}</label>
              <textarea style={{ ...input, resize: 'vertical', minHeight: 64 }} value={instruction} onChange={e => setInstruction(e.target.value)} />
            </div>
            <div style={field}>
              <label style={label}>{t('workers.create.readme')}</label>
              <textarea style={{ ...input, resize: 'vertical', minHeight: 48 }} value={readme} onChange={e => setReadme(e.target.value)} placeholder={t('workers.create.readme.placeholder')} />
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                {t('workers.create.readme.hint')}
              </div>
            </div>
            <div style={{ display: 'flex', gap: 8 }}>
              <div style={{ ...field, flex: 1 }}>
                <label style={label}>{t('workers.create.provider')}</label>
                <input style={input} value={provider} onChange={e => setProvider(e.target.value)} placeholder={t('workers.create.provider.placeholder')} />
              </div>
              <div style={{ ...field, flex: 1 }}>
                <label style={label}>{t('workers.create.model')}</label>
                <input style={input} value={model} onChange={e => setModel(e.target.value)} />
              </div>
            </div>

            {/* Spawn seeding: the two knobs that decide what the worker starts
                with — its standing goal (prompt space) and its handover brief
                (working material). */}
            <div style={field}>
              <label style={label}>{t('workers.create.goal')}</label>
              <textarea style={{ ...input, resize: 'vertical', minHeight: 48 }} value={goal} onChange={e => setGoal(e.target.value)} placeholder={t('workers.create.goalPlaceholder')} />
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                {t('workers.create.goal.hint')}
              </div>
            </div>
            <div style={field}>
              <label style={label}>{t('workers.create.brief')}</label>
              <textarea style={{ ...input, resize: 'vertical', minHeight: 48 }} value={brief} onChange={e => setBrief(e.target.value)} placeholder={t('workers.create.briefPlaceholder')} />
            </div>

            {effType === 'coding' && (
              <>
                <div style={field}>
                  <label style={label}>{t('workers.create.toolMode')}</label>
                  <div style={{ display: 'flex', gap: 6 }}>
                    {(['normal', 'ptc'] as const).map(m => (
                      <span key={m} style={pill(toolMode === m)} onClick={() => setToolMode(m)}>{m}</span>
                    ))}
                  </div>
                  <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                    {toolMode === 'ptc' ? t('workers.create.toolMode.ptcHint') : t('workers.create.toolMode.normalHint')}
                  </div>
                </div>
              </>
            )}
          </>
        )}
        {isManaged && (effType === 'workspace' || effType === 'program') && (
          <div style={field}>
            <label style={label}>{t('workers.create.rootDir')}</label>
            <input style={input} value={rootDir} onChange={e => setRootDir(e.target.value)} />
          </div>
        )}

        {isManaged && effType === 'niw' && (
          <>
            <div style={field}>
              <label style={label}>{t('workers.create.niwConfig')}</label>
              <textarea
                style={{ ...input, resize: 'vertical', minHeight: 72 }}
                value={niwConfig}
                onChange={e => setNiwConfig(e.target.value)}
                placeholder={t('workers.create.niwConfigPlaceholder')}
              />
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                {t('workers.create.niwHint')}
              </div>
            </div>
            <div style={field}>
              <label style={label}>{t('workers.create.niwRecipient')}</label>
              <input style={input} value={niwRecipient} onChange={e => setNiwRecipient(e.target.value)} placeholder="reason.local" />
            </div>
          </>
        )}

        {isManaged && effType === 'mcp' && (
          <>
            <div style={field}>
              <label style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
                <input type="checkbox" checked={mcpFixed} onChange={e => setMcpFixed(e.target.checked)} />
                <span style={{ fontSize: fontSizes.sm, color: colors.text }}>Fixed tool provider</span>
              </label>
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                {'Config-only: connect at start and expose tools of the configured servers. Disables runtime ' +
                  'server add/remove/update and expose switching; the config is authoritative across restarts.'}
              </div>
            </div>
            <div style={field}>
              <label style={label}>Expose</label>
              <select style={input} value={mcpExpose} onChange={e => setMcpExpose(e.target.value)}>
                <option value="agentic">agentic (list/call on demand)</option>
                <option value="ready">ready (flat tool exposure)</option>
              </select>
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                agentic: mcp.server.list / mcp.tool.list / mcp.tool.call · ready: each remote tool as its own tool
              </div>
            </div>
            <div style={field}>
              <label style={label}>MCP servers (JSON array)</label>
              <textarea
                style={{ ...input, resize: 'vertical', minHeight: 96, fontFamily: 'monospace' }}
                value={mcpServersText}
                onChange={e => setMcpServersText(e.target.value)}
                placeholder={'[{"name":"github","url":"https://api.githubcopilot.com/mcp","protocol":"streamable-http","headers":{"Authorization":"Bearer ${GITHUB_TOKEN}"}}]'}
              />
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                {'Each entry: name, url, optional protocol / headers. Header values may reference ${ENV_VAR}.'}
              </div>
            </div>
            <div style={field}>
              <label style={label}>Allowed tools (optional)</label>
              <input
                style={input}
                value={mcpAllowedTools}
                onChange={e => setMcpAllowedTools(e.target.value)}
                placeholder="comma-separated tool names; empty = all"
              />
              <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
                {'If set, only these tools (by name) are listed and callable.'}
              </div>
            </div>
          </>
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
              {envRows.map((row, i) => (
                <div key={i} style={{ display: 'flex', gap: 6, marginBottom: 6, alignItems: 'center' }}>
                  <input
                    style={{ ...input, width: 'auto', flex: '0 0 40%' }}
                    value={row.key}
                    placeholder={t('workers.create.env.key')}
                    onChange={e => setEnvRows(envRows.map((r, j) => j === i ? { ...r, key: e.target.value } : r))}
                  />
                  <input
                    style={{ ...input, width: 'auto', flex: 1, minWidth: 0 }}
                    value={row.value}
                    placeholder={t('workers.create.env.value')}
                    onChange={e => setEnvRows(envRows.map((r, j) => j === i ? { ...r, value: e.target.value } : r))}
                  />
                  <span
                    onClick={() => setEnvRows(envRows.filter((_, j) => j !== i))}
                    className="btn-hover"
                    title={t('wd.remove')}
                    style={{ cursor: 'pointer', color: colors.textDimmed, fontSize: fontSizes.md, userSelect: 'none', padding: '0 4px' }}
                  >
                    {'\u2715'}
                  </span>
                </div>
              ))}
              <span
                onClick={() => setEnvRows([...envRows, { key: '', value: '' }])}
                className="btn-hover"
                style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 2, padding: '3px 10px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
              >
                {t('wd.add')}
              </span>
            </div>
          </>
        )}

        {(isExternal || isRemote) && (
          <div style={field}>
            <label style={label}>{t('workers.create.tokenTtl')}</label>
            <input style={input} value={tokenTtl} onChange={e => setTokenTtl(e.target.value)} placeholder="30 / 180" />
            <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }}>
              {t('workers.create.tokenTtlHint')}
            </div>
          </div>
        )}

        {/* Subscription / publish grants: one module for all three worker
            kinds, collapsed to a single 权限管理 button until opened. */}
        <div style={field}>
          <PermissionPanel
            publish={publish}
            subscribe={subscriptions}
            onPublishChange={setPublish}
            onSubscribeChange={setSubscriptions}
            workers={workers}
          />
        </div>
        </>)}

        {note && (
          <div style={{ fontSize: fontSizes.sm, color: colors.toolFailed, marginBottom: 10, lineHeight: 1.5, wordBreak: 'break-all' }}>{note}</div>
        )}

        {/* Connect configuration for a freshly-created remote worker: a detail
            card with the credential and the SDK-ready env block, both copyable.
            The form fields are replaced by this block once created. */}
        {result && (
          <div style={{ marginBottom: 12, padding: '10px 12px', background: colors.detailBg, borderRadius: 6 }}>
            <div style={{ fontSize: fontSizes.sm, color: colors.textDim, marginBottom: 8, lineHeight: 1.5 }}>
              {t('workers.create.remoteConfig.desc')}
            </div>
            {/* Copyable worker id + credential. */}
            <div style={{ display: 'flex', gap: 8, alignItems: 'center', marginBottom: 8, flexWrap: 'wrap' }}>
              <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{t('wd.id')}:</span>
              <code style={{ fontSize: fontSizes.xs, color: colors.text, background: colors.bg, border: '1px solid ' + colors.border, borderRadius: 3, padding: '2px 6px' }}>{result.id}</code>
              <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{t('wd.credential')}:</span>
              <code style={{ fontSize: fontSizes.xs, color: colors.text, background: colors.bg, border: '1px solid ' + colors.border, borderRadius: 3, padding: '2px 6px', wordBreak: 'break-all' }}>{result.credential}</code>
              <span
                onClick={copyCredential}
                className="btn-hover"
                style={{ cursor: 'pointer', border: '1px solid ' + colors.border, borderRadius: 3, padding: '2px 8px', color: colors.textDim, fontSize: fontSizes.xs, userSelect: 'none' }}
              >
                {t('workers.create.copyCredential')}
              </span>
            </div>
            <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4, lineHeight: 1.5 }}>
              {t('workers.create.remoteConfig.sdk')}
            </div>
            <pre style={{ margin: 0, padding: 8, background: colors.bg, border: '1px solid ' + colors.border, borderRadius: 4, fontSize: fontSizes.xs, color: colors.text, whiteSpace: 'pre-wrap', wordBreak: 'break-all', lineHeight: 1.6 }}>{remoteConfig()}</pre>
            <span
              onClick={copyRemoteConfig}
              className="btn-hover"
              style={{ cursor: 'pointer', display: 'inline-block', marginTop: 8, border: '1px solid ' + colors.accent, borderRadius: 3, padding: '3px 12px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
            >
              {t('workers.create.copyConfig')}
            </span>
            {copied && (
              <span style={{ fontSize: fontSizes.xs, color: colors.toolCompleted, marginLeft: 10 }}>{t('workers.create.copied')}</span>
            )}
          </div>
        )}
        </div>

        {/* Pinned action row. */}
        <div style={{ flexShrink: 0, padding: '10px 16px', borderTop: '1px solid ' + colors.border }}>
	{result ? (
	  /* Remote worker created: just a close to dismiss the config card. */
	  <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
	    <span
	      onClick={onClose}
	      className="btn-hover"
	      style={{ cursor: 'pointer', border: '1px solid ' + colors.accent, borderRadius: 4, padding: '4px 14px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
	    >
	      {t('wd.done')}
	    </span>
	  </div>
	) : (
	  <div style={{ display: 'flex', gap: 8 }}>
	    <span
	      onClick={submit}
	      className="btn-hover"
	      style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, border: '1px solid ' + colors.accent, borderRadius: 4, padding: '4px 14px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
	    >
	      {busy ? t('workers.create.saving') : onBuild ? t('templates.addWorker.submit') : isRemote ? t('workers.create.submitRemote') : t('workers.create.submit')}
	    </span>
	    <span
	      onClick={onClose}
	      className="btn-hover"
	      style={{ cursor: 'pointer', border: '1px solid ' + colors.border, borderRadius: 2, padding: '4px 14px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
	    >
	      {t('wd.cancel')}
	    </span>
	  </div>
	)}
        </div>
      </div>
    </>
  )
}
