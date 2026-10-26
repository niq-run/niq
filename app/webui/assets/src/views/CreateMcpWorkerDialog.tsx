import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { createWorker } from '../services/api'
import { isValidMcpURL, parseHeaders, splitList } from '../components/mcp-utils'

interface CreateMcpWorkerDialogProps {
  open: boolean
  onClose: () => void
  // onCreated receives the new worker's id so the caller can select it.
  onCreated: (id: string) => void
}

// One server being composed in the form. `key` is UI-only (a stable React key);
// the worker itself only ever sees name/url/protocol/headers.
interface DraftServer {
  key: number
  name: string
  url: string
  protocol: string
  headers: string
}

let draftKey = 1
function emptyServer(): DraftServer {
  return { key: draftKey++, name: '', url: '', protocol: 'streamable-http', headers: '' }
}

// CreateMcpWorkerDialog is the MCP view's own creation form. It is deliberately
// NOT the generic worker form: an mcp worker's meaningful inputs are its server
// set and its exposure, so the form composes several servers directly instead of
// asking for a JSON blob. It always creates a managed worker (mcp is a built-in
// in-process worker type).
export default function CreateMcpWorkerDialog({ open, onClose, onCreated }: CreateMcpWorkerDialogProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [id, setId] = useState('')
  const [description, setDescription] = useState('')
  const [tags, setTags] = useState('')
  const [fixed, setFixed] = useState(false)
  const [expose, setExpose] = useState<'agentic' | 'ready'>('agentic')
  const [allowed, setAllowed] = useState('')
  const [servers, setServers] = useState<DraftServer[]>([emptyServer()])
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')

  if (!open) return null

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
    marginBottom: 3,
  }
  const field: React.CSSProperties = { marginBottom: 10 }
  const hint: React.CSSProperties = { fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4, lineHeight: 1.5 }
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

  const patchServer = (key: number, patch: Partial<DraftServer>) => {
    setServers((prev) => prev.map((s) => (s.key === key ? { ...s, ...patch } : s)))
  }

  const reset = () => {
    setId('')
    setDescription('')
    setTags('')
    setFixed(false)
    setExpose('agentic')
    setAllowed('')
    setServers([emptyServer()])
    setNote('')
  }

  const submit = async () => {
    if (busy) return
    setNote('')

    const workerID = id.trim()
    if (!/^[A-Za-z0-9._-]+$/.test(workerID)) {
      setNote(t('workers.create.errId'))
      return
    }

    // Only rows the user actually started filling count; a half-filled row is
    // an error rather than silently dropped.
    const filled = servers.filter((s) => s.name.trim() || s.url.trim() || s.headers.trim())
    const list: Record<string, unknown>[] = []
    const seen = new Set<string>()
    for (const s of filled) {
      const name = s.name.trim()
      const url = s.url.trim()
      if (!name || !url) {
        setNote(t('mcp.create.errServerFields'))
        return
      }
      if (!isValidMcpURL(url)) {
        setNote(t('mcp.create.errServerUrl', { name }))
        return
      }
      if (seen.has(name)) {
        setNote(t('mcp.create.errDuplicate', { name }))
        return
      }
      seen.add(name)
      const headers = parseHeaders(s.headers)
      list.push({ name, url, protocol: s.protocol, ...(headers ? { headers } : {}) })
    }
    // A fixed worker is config-only: it cannot add servers at runtime, so an
    // empty server set would make it a no-op forever.
    if (fixed && list.length === 0) {
      setNote(t('mcp.create.errFixedNoServer'))
      return
    }

    const allowedTools = splitList(allowed)
    const params: Record<string, unknown> = { fixed, expose, servers: list }
    if (allowedTools.length > 0) params.allowed_tools = allowedTools

    const body: Record<string, unknown> = { type: 'mcp', id: workerID, managed: true, params }
    const tagList = splitList(tags)
    if (tagList.length > 0) body.tags = tagList
    if (description.trim()) body.description = description.trim()

    setBusy(true)
    try {
      await createWorker(body)
      reset()
      onCreated(workerID)
    } catch (e) {
      setNote((e as Error)?.message || 'create failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      {/* Backdrop: tapping it closes the dialog. */}
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', zIndex: 90 }} />
      <div
        onClick={(e) => e.stopPropagation()}
        style={{
          position: 'fixed',
          top: '50%',
          left: '50%',
          transform: 'translate(-50%, -50%)',
          width: 'min(560px, calc(100vw - 32px))',
          maxHeight: '86vh',
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
          <span style={{ color: colors.text, fontSize: fontSizes.md }}>{t('mcp.create.title')}</span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{
              cursor: 'pointer',
              marginLeft: 'auto',
              border: '1px solid ' + colors.border,
              borderRadius: 2,
              padding: '0 8px',
              color: colors.textDim,
              fontSize: fontSizes.sm,
              lineHeight: '20px',
              userSelect: 'none',
            }}
          >
            {'\u2715'}
          </span>
        </div>

        <div style={{ overflowY: 'auto', flex: 1, minHeight: 0, padding: '0 16px 14px' }}>
          <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, lineHeight: 1.5, marginBottom: 12 }}>
            {t('mcp.create.intro')}
          </div>

          <div style={field}>
            <label style={label}>{t('mcp.create.id')}</label>
            <input style={input} value={id} onChange={(e) => setId(e.target.value)} placeholder="mcp" />
          </div>
          <div style={field}>
            <label style={label}>{t('mcp.create.description')}</label>
            <input
              style={input}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder={t('mcp.create.descriptionPlaceholder')}
            />
          </div>
          <div style={field}>
            <label style={label}>{t('mcp.create.tags')}</label>
            <input style={input} value={tags} onChange={(e) => setTags(e.target.value)} placeholder="tools/github, tools/search" />
            <div style={hint}>{t('mcp.create.tagsHint')}</div>
          </div>

          {/* Mode: dynamic (manageable at runtime) vs fixed (config-only). */}
          <div style={field}>
            <label style={label}>{t('mcp.mode.label')}</label>
            <div style={{ display: 'flex', gap: 6 }}>
              {(['dynamic', 'fixed'] as const).map((m) => (
                <span key={m} style={pill((m === 'fixed') === fixed)} onClick={() => setFixed(m === 'fixed')}>
                  {m === 'fixed' ? t('mcp.mode.fixed') : t('mcp.mode.dynamic')}
                </span>
              ))}
            </div>
            <div style={hint}>{fixed ? t('mcp.mode.fixedHint') : t('mcp.mode.dynamicHint')}</div>
          </div>

          {/* Exposure: how the tools surface on the bus. Applies in both modes. */}
          <div style={field}>
            <label style={label}>{t('mcp.exposure')}</label>
            <div style={{ display: 'flex', gap: 6 }}>
              {(['agentic', 'ready'] as const).map((m) => (
                <span key={m} style={pill(expose === m)} onClick={() => setExpose(m)}>
                  {m === 'agentic' ? t('mcp.expose.agentic') : t('mcp.expose.ready')}
                </span>
              ))}
            </div>
            <div style={hint}>{expose === 'agentic' ? t('mcp.expose.agenticHint') : t('mcp.expose.readyHint')}</div>
          </div>

          <div style={field}>
            <label style={label}>{t('mcp.create.allowedTools')}</label>
            <input
              style={input}
              value={allowed}
              onChange={(e) => setAllowed(e.target.value)}
              placeholder={t('mcp.create.allowedToolsPlaceholder')}
            />
            <div style={hint}>{t('mcp.create.allowedToolsHint')}</div>
          </div>

          {/* The server set: several per worker, composed one card at a time. */}
          <div style={{ marginBottom: 6 }}>
            <label style={label}>{t('mcp.create.servers')}</label>
            <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, lineHeight: 1.5, marginBottom: 8 }}>
              {t('mcp.create.serversHint')}
            </div>
          </div>

          {servers.map((s, i) => (
            <div
              key={s.key}
              style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: 10, marginBottom: 8, background: colors.bg }}
            >
              <div style={{ display: 'flex', alignItems: 'center', marginBottom: 8 }}>
                <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{t('mcp.create.serverN', { n: i + 1 })}</span>
                <span
                  onClick={() => setServers((prev) => prev.filter((x) => x.key !== s.key))}
                  className="btn-hover"
                  style={{ marginLeft: 'auto', cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.xs, userSelect: 'none' }}
                >
                  {t('mcp.create.removeServer')}
                </span>
              </div>

              <div style={{ display: 'flex', gap: 8 }}>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <label style={label}>{t('mcp.server.name')}</label>
                  <input style={input} value={s.name} onChange={(e) => patchServer(s.key, { name: e.target.value })} placeholder="github" />
                </div>
                <div style={{ width: 168, flexShrink: 0 }}>
                  <label style={label}>{t('mcp.server.protocol')}</label>
                  <select style={input} value={s.protocol} onChange={(e) => patchServer(s.key, { protocol: e.target.value })}>
                    <option value="streamable-http">streamable-http</option>
                    {/* Local stdio MCP is not supported by the worker yet. */}
                    <option value="local-stdio" disabled>
                      {t('mcp.server.protocolLocal')}
                    </option>
                  </select>
                </div>
              </div>

              <div style={{ marginTop: 8 }}>
                <label style={label}>{t('mcp.server.url')}</label>
                <input
                  style={input}
                  value={s.url}
                  onChange={(e) => patchServer(s.key, { url: e.target.value })}
                  placeholder="https://api.githubcopilot.com/mcp"
                />
              </div>

              <div style={{ marginTop: 8 }}>
                <label style={label}>{t('mcp.server.headers')}</label>
                <textarea
                  style={{ ...input, minHeight: 48, resize: 'vertical', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace' }}
                  value={s.headers}
                  onChange={(e) => patchServer(s.key, { headers: e.target.value })}
                  placeholder={t('mcp.server.headers.placeholder')}
                />
                <div style={hint}>{t('mcp.server.headers.hint')}</div>
              </div>
            </div>
          ))}

          <span
            onClick={() => setServers((prev) => [...prev, emptyServer()])}
            className="btn-hover"
            style={{
              cursor: 'pointer',
              display: 'inline-block',
              border: '1px dashed ' + colors.border,
              borderRadius: 4,
              padding: '4px 12px',
              color: colors.textDim,
              fontSize: fontSizes.sm,
              userSelect: 'none',
            }}
          >
            {t('mcp.create.addServer')}
          </span>

          <div style={{ ...hint, marginTop: 10 }}>{t('mcp.localNote')}</div>
          {note && <div style={{ fontSize: fontSizes.sm, color: colors.toolFailed, marginTop: 10, lineHeight: 1.5, wordBreak: 'break-word' }}>{note}</div>}
        </div>

        <div style={{ flexShrink: 0, padding: '10px 16px', borderTop: '1px solid ' + colors.border, display: 'flex', gap: 8 }}>
          <span
            onClick={submit}
            className="btn-hover"
            style={{
              cursor: busy ? 'default' : 'pointer',
              opacity: busy ? 0.6 : 1,
              border: '1px solid ' + colors.accent,
              borderRadius: 4,
              padding: '4px 14px',
              color: colors.accent,
              fontSize: fontSizes.sm,
              userSelect: 'none',
            }}
          >
            {busy ? t('mcp.create.saving') : t('mcp.create.submit')}
          </span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{
              cursor: 'pointer',
              border: '1px solid ' + colors.border,
              borderRadius: 2,
              padding: '4px 14px',
              color: colors.textDim,
              fontSize: fontSizes.sm,
              userSelect: 'none',
            }}
          >
            {t('mcp.server.cancel')}
          </span>
        </div>
      </div>
    </>
  )
}
