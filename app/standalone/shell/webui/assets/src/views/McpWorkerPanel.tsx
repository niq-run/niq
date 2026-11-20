import { useEffect, useMemo, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo, type McpServerInfo, type McpToolInfo } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'
import {
  mcpDeclared,
  mcpServersParam,
  mcpWriteParams,
  parseHeaders,
  formatHeaders,
  isValidMcpURL,
  splitList,
  type DeclaredMcpServer,
} from '../components/mcp-utils'
import {
  Action,
  Chip,
  Field,
  Pill,
  ProtocolSelect,
  Row,
  Section,
  errText,
  hintStyle,
  inputStyle,
  labelStyle,
  monoFont,
  preStyle,
} from '../components/mcp-ui'
import {
  mcpListServers,
  mcpListTools,
  mcpAddServer,
  mcpUpdateServer,
  mcpRemoveServer,
  mcpReadExpose,
  mcpSetExpose,
  startWorker,
  restartWorker,
  updateWorkerParams,
} from '../services/api'

// DraftServer is one server being edited. `orig` is the existing server's name
// (undefined for a newly added row); the o* fields are what the draft started
// from, so saving replays only what actually changed.
interface DraftServer {
  key: number
  orig?: string
  name: string
  url: string
  protocol: string
  headers: string
  oURL: string
  oProtocol: string
  oHeaders: string
}

let draftKey = 1

// McpWorkerPanel is one mcp worker: its exposure mode and its server set.
//
// Every detail is edited through one explicit EDIT → SAVE flow (nothing applies
// on a bare click). Saving takes one of two paths, per worker:
//   - the declaration (params) + restart for a FIXED worker — it registers no
//     management events at all — and for an OFFLINE worker, which cannot answer;
//   - live bus events (mcp.server.add/remove/update, mcp.expose), replayed as a
//     diff of the draft, for a running dynamic worker.
// The allowlist is always build-time input: it is written to the declaration and
// applied on the worker's next restart.
export default function McpWorkerPanel({
  worker,
  isMobile,
  onRefresh,
  reloadSignal,
}: {
  worker: WorkerInfo
  isMobile: boolean
  onRefresh?: () => void
  reloadSignal: number
}) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const online = worker.online !== false
  const decl = useMemo(() => mcpDeclared(worker.params), [worker.params])

  // The declaration as we persist it back (flat; a legacy nested params.mcp is
  // merged in). A ref so consecutive saves compose instead of racing the poll.
  const paramsRef = useRef<Record<string, unknown>>(mcpWriteParams(worker.params))

  const [servers, setServers] = useState<McpServerInfo[]>(decl.servers)
  const [expose, setExpose] = useState(decl.expose)
  const [allowedTools, setAllowedTools] = useState<string[]>(decl.allowedTools)
  const [loading, setLoading] = useState(online)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState('')
  const [nonce, setNonce] = useState(0)

  const useDecl = decl.fixed || !online
  // A declared-but-stopped worker cannot be restarted into existence: its edits
  // wait for the start button.
  const canRestart = online && !(!!worker.managed && worker.state === 'stopped')

  // Live server set, plus the live exposure mode for a dynamic worker (it can be
  // switched at runtime, so the declaration may be stale). In fixed mode
  // mcp.expose is not registered at all, so the declaration is authoritative.
  useEffect(() => {
    if (!online) {
      setServers(decl.servers)
      setExpose(decl.expose)
      setAllowedTools(decl.allowedTools)
      setLoading(false)
      return
    }
    let cancelled = false
    const run = async () => {
      setLoading(true)
      setError('')
      try {
        const list = await mcpListServers(worker.id)
        if (cancelled) return
        if (!list.ok) throw new Error(list.error || 'mcp.server.list failed')
        setServers(Array.isArray(list.result) ? (list.result as McpServerInfo[]) : [])
        if (!decl.fixed) {
          const cur = await mcpReadExpose(worker.id).catch(() => null)
          if (!cancelled && cur?.ok) {
            const mode = (cur.result as { mode?: unknown } | undefined)?.mode
            if (typeof mode === 'string' && mode) setExpose(mode)
          }
        }
      } catch (e) {
        if (!cancelled) setError(errText(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void run()
    return () => {
      cancelled = true
    }
    // decl is deliberately not a dependency: its identity changes on every
    // worker-list poll while its meaningful values do not.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [worker.id, online, decl.fixed, nonce, reloadSignal])

  // ── draft ──
  const [editing, setEditing] = useState(false)
  const [draftExpose, setDraftExpose] = useState(decl.expose)
  const [draftAllowed, setDraftAllowed] = useState('')
  const [draftServers, setDraftServers] = useState<DraftServer[]>([])

  // The declaration's servers, with their headers — the only place headers are
  // visible, since mcp.server.list does not report them.
  const declaredServers = (): DeclaredMcpServer[] => mcpServersParam(paramsRef.current)
  const declaredHeaders = (name: string): string => formatHeaders(declaredServers().find((d) => d.name === name)?.headers)

  const startEditing = () => {
    setError('')
    setNote('')
    setDraftExpose(expose)
    setDraftAllowed(allowedTools.join(', '))
    setDraftServers(
      servers.map((s) => {
        const protocol = s.protocol || 'streamable-http'
        const headers = declaredHeaders(s.name)
        return {
          key: draftKey++,
          orig: s.name,
          name: s.name,
          url: s.url,
          protocol,
          headers,
          oURL: s.url,
          oProtocol: protocol,
          oHeaders: headers,
        }
      }),
    )
    setEditing(true)
  }

  const cancelEditing = () => {
    setEditing(false)
    setError('')
    setNote('')
  }

  const patchDraft = (key: number, patch: Partial<DraftServer>) => {
    setDraftServers((prev) => prev.map((r) => (r.key === key ? { ...r, ...patch } : r)))
  }

  const addDraftServer = () => {
    setDraftServers((prev) => [
      ...prev,
      {
        key: draftKey++,
        name: '',
        url: '',
        protocol: 'streamable-http',
        headers: '',
        oURL: '',
        oProtocol: 'streamable-http',
        oHeaders: '',
      },
    ])
  }

  // declareAndApply merges a params patch into the declaration, persists it and
  // (unless told otherwise) restarts the worker, since params are build-time
  // input. This is the only editing path a fixed worker has.
  const declareAndApply = async (
    patch: Record<string, unknown>,
    opts: { restart?: boolean } = {},
  ): Promise<boolean> => {
    if (busy) return false
    setBusy('save')
    setError('')
    setNote('')
    const next = { ...paramsRef.current, ...patch }
    try {
      await updateWorkerParams(worker.id, next)
      paramsRef.current = next
      if ('servers' in patch && Array.isArray(patch.servers)) setServers(patch.servers as McpServerInfo[])
      if (typeof patch.expose === 'string') setExpose(patch.expose)
      if ('allowed_tools' in patch) {
        setAllowedTools(Array.isArray(patch.allowed_tools) ? (patch.allowed_tools as unknown[]).map(String) : [])
      }
      let restarted = false
      if (opts.restart !== false && canRestart) {
        try {
          await restartWorker(worker.id)
          restarted = true
        } catch {
          // Persisted but not applied yet — the restart button retries.
        }
      }
      setNote(restarted ? t('mcp.decl.savedRestarted') : t('mcp.decl.saved'))
      onRefresh?.()
      // Re-read the live list once a restarted worker is back on the bus.
      setTimeout(() => setNonce((n) => n + 1), restarted ? 2000 : 300)
      return true
    } catch (e) {
      setError(errText(e))
      return false
    } finally {
      setBusy('')
    }
  }

  // saveEdits validates the draft, then applies it. A failure stops the run with
  // a precise message and keeps the draft open for a retry.
  const saveEdits = async () => {
    if (busy) return
    setError('')
    setNote('')

    const rows = draftServers.filter((r) => r.name.trim() || r.url.trim() || r.headers.trim())
    const seen = new Set<string>()
    for (const r of rows) {
      const name = r.name.trim()
      const url = r.url.trim()
      if (!name || !url) {
        setError(t('mcp.err.nameUrl'))
        return
      }
      if (!isValidMcpURL(url)) {
        setError(t('mcp.err.url'))
        return
      }
      if (seen.has(name)) {
        setError(t('mcp.errDuplicate', { name }))
        return
      }
      seen.add(name)
    }
    const allowedList = splitList(draftAllowed)
    const allowedChanged = allowedList.join('\n') !== allowedTools.join('\n')

    if (useDecl) {
      // The declaration rewrite is the whole change; a missing headers key means
      // "no headers", because the server list is replaced wholesale.
      const out = rows.map((r) => {
        const h = parseHeaders(r.headers)
        return { name: r.name.trim(), url: r.url.trim(), protocol: r.protocol, ...(h ? { headers: h } : {}) }
      })
      if (await declareAndApply({ expose: draftExpose, servers: out, allowed_tools: allowedList })) {
        setEditing(false)
      }
      return
    }

    setBusy('save')
    try {
      if (draftExpose !== expose) {
        const r = await mcpSetExpose(worker.id, draftExpose)
        if (!r.ok) throw new Error(r.error || 'mcp.expose failed')
      }
      // Removals first, so a later add can reuse the freed name.
      for (const s of servers) {
        if (seen.has(s.name)) continue
        const r = await mcpRemoveServer(worker.id, s.name)
        if (!r.ok) throw new Error(r.error || 'mcp.server.remove failed')
      }
      for (const r of rows) {
        const name = r.name.trim()
        if (!r.orig) {
          const h = parseHeaders(r.headers)
          const res = await mcpAddServer(worker.id, {
            name,
            url: r.url.trim(),
            protocol: r.protocol,
            ...(h ? { headers: h } : {}),
          })
          if (!res.ok) throw new Error(res.error || 'mcp.server.add failed')
          continue
        }
        if (r.url.trim() === r.oURL && r.protocol === r.oProtocol && r.headers === r.oHeaders) continue
        const h = parseHeaders(r.headers)
        const res = await mcpUpdateServer(worker.id, {
          name,
          url: r.url.trim(),
          protocol: r.protocol,
          // The headers field was prefilled, so empty here means "clear them".
          ...(h ? { headers: h } : { headers: {} }),
        })
        if (!res.ok) throw new Error(res.error || 'mcp.server.update failed')
      }

      // The allowlist is build-time only: write it to the declaration and let
      // the next restart apply it. No restart is forced here — the live server
      // set/exposure we just changed lives in the worker's snapshot, and a
      // restart racing an unfinished checkpoint could roll it back.
      if (allowedChanged) {
        const next = { ...paramsRef.current, allowed_tools: allowedList }
        await updateWorkerParams(worker.id, next)
        paramsRef.current = next
      }
      setExpose(draftExpose)
      setAllowedTools(allowedList)
      setEditing(false)
      setNote(allowedChanged ? t('mcp.savedAllowlistRestart') : t('mcp.saved'))
      onRefresh?.()
      setNonce((n) => n + 1)
    } catch (e) {
      setError(errText(e))
    } finally {
      setBusy('')
    }
  }

  // ── tools (read-only, fetched on demand) ──
  const [openServer, setOpenServer] = useState<string | null>(null)
  const [toolsByServer, setToolsByServer] = useState<Record<string, McpToolInfo[]>>({})
  const [toolsBusy, setToolsBusy] = useState('')
  const [toolsErr, setToolsErr] = useState<Record<string, string>>({})

  const loadTools = async (name: string) => {
    if (toolsBusy === name) return
    setToolsBusy(name)
    setToolsErr((prev) => ({ ...prev, [name]: '' }))
    try {
      const r = await mcpListTools(worker.id, name)
      if (!r.ok) throw new Error(r.error || 'mcp.tool.list failed')
      const tools = Array.isArray(r.result) ? (r.result as McpToolInfo[]) : []
      setToolsByServer((prev) => ({ ...prev, [name]: tools }))
      // A successful tools/list means the worker has a live MCP session for
      // this server. The server list is only a snapshot fetched on open or
      // refresh, so fold that fact back into it: otherwise the status text
      // stays "configured" after a tool list that clearly worked.
      setServers((prev) => prev.map((s) => (
        s.name === name ? { ...s, status: 'connected', tools: tools.length } : s
      )))
    } catch (e) {
      setToolsErr((prev) => ({ ...prev, [name]: errText(e) }))
    } finally {
      setToolsBusy('')
    }
  }

  const toggleTools = (name: string) => {
    if (openServer === name) {
      setOpenServer(null)
      return
    }
    setOpenServer(name)
    if (!toolsByServer[name] && toolsBusy !== name) void loadTools(name)
  }

  // ── lifecycle ──
  const [lifeBusy, setLifeBusy] = useState(false)
  const stopped = !!worker.managed && worker.state === 'stopped'
  const lifeAction = async () => {
    if (lifeBusy || busy) return
    setLifeBusy(true)
    setError('')
    setNote('')
    try {
      if (stopped) await startWorker(worker.id)
      else await restartWorker(worker.id)
      setNote(stopped ? t('mcp.lifecycle.starting') : t('mcp.lifecycle.restarting'))
      onRefresh?.()
    } catch (e) {
      setError(errText(e))
    } finally {
      setLifeBusy(false)
    }
  }

  const typeColor = worker.type ? getWorkerTypeColor(worker.type, colors) : colors.textDimmed
  const shownExpose = editing ? draftExpose : expose

  return (
    <div style={{ padding: isMobile ? 14 : '16px 20px', display: 'flex', flexDirection: 'column', gap: 12 }}>
      {/* Identity + live status. Actions follow the edit state: an explicit Edit
          first, then Save/Cancel — nothing applies on a bare click. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        <span
          title={online ? t('worker.online') : t('worker.offline')}
          style={{ width: 8, height: 8, borderRadius: '50%', flexShrink: 0, background: online ? colors.toolCompleted : colors.textDimmed }}
        />
        <strong style={{ color: colors.text, fontSize: fontSizes.lg }}>{worker.id}</strong>
        {worker.type && (
          <span
            style={{
              display: 'inline-block',
              padding: '0 6px',
              borderRadius: 4,
              fontSize: fontSizes.xs,
              lineHeight: '18px',
              color: typeColor,
              background: typeColor + '1f',
              border: '1px solid ' + typeColor + '55',
            }}
          >
            {worker.type}
          </span>
        )}
        <span style={{ color: online ? colors.textDim : colors.textDimmed, fontSize: fontSizes.sm }}>
          {online ? t('worker.online') : t('worker.offline')}
        </span>
        {worker.description && (
          <span style={{ color: colors.textDim, fontSize: fontSizes.sm, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {'\u00b7 ' + worker.description}
          </span>
        )}
        <span style={{ marginLeft: 'auto', display: 'flex', gap: 8, alignItems: 'center', flexShrink: 0 }}>
          {editing ? (
            <>
              <Pill onClick={() => void saveEdits()} colors={colors} label={busy ? t('mcp.saving') : t('mcp.save')} accent />
              <Pill onClick={busy ? undefined : cancelEditing} colors={colors} label={t('mcp.server.cancel')} dim />
            </>
          ) : (
            <>
              <Pill onClick={startEditing} colors={colors} label={t('mcp.edit')} accent />
              {worker.managed && (
                <Pill
                  onClick={lifeBusy ? undefined : lifeAction}
                  colors={colors}
                  label={lifeBusy ? (stopped ? t('mcp.lifecycle.starting') : t('mcp.lifecycle.restarting')) : stopped ? t('mcp.lifecycle.start') : t('mcp.lifecycle.restart')}
                  dim
                />
              )}
              <Pill onClick={() => setNonce((n) => n + 1)} colors={colors} label={t('workers.refresh')} dim />
            </>
          )}
        </span>
      </div>

      {error && <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm, lineHeight: 1.5, wordBreak: 'break-word' }}>{error}</div>}
      {note && <div style={{ color: colors.toolCompleted, fontSize: fontSizes.sm, lineHeight: 1.5 }}>{note}</div>}

      {/* Mode + exposure + allowlist. */}
      <Section title={t('mcp.exposure')} colors={colors}>
        <Row>
          <span style={labelStyle(colors)}>{t('mcp.mode.label')}</span>
          <Chip colors={colors}>{decl.fixed ? t('mcp.mode.fixed') : t('mcp.mode.dynamic')}</Chip>
          <span style={hintStyle(colors)}>{decl.fixed ? t('mcp.mode.fixedHint') : t('mcp.mode.dynamicHint')}</span>
        </Row>

        <Row style={{ marginTop: 10 }}>
          <span style={labelStyle(colors)}>{t('mcp.exposure')}</span>
          {(['agentic', 'ready'] as const).map((m) => (
            <Chip
              key={m}
              colors={colors}
              active={shownExpose === m}
              onClick={editing ? () => setDraftExpose(m) : undefined}
            >
              {m === 'agentic' ? t('mcp.expose.agentic') : t('mcp.expose.ready')}
            </Chip>
          ))}
          {busy === 'save' && <span style={hintStyle(colors)}>{t('mcp.saving')}</span>}
        </Row>
        <div style={{ ...hintStyle(colors), marginTop: 6 }}>
          {shownExpose === 'ready' ? t('mcp.expose.readyHint') : t('mcp.expose.agenticHint')}
        </div>

        <Row style={{ marginTop: 10 }}>
          <span style={labelStyle(colors)}>{t('mcp.allowedTools')}</span>
          {editing ? (
            <input
              style={{ ...inputStyle(colors), flex: 1, minWidth: 220 }}
              value={draftAllowed}
              onChange={(e) => setDraftAllowed(e.target.value)}
              placeholder={t('mcp.allowedTools.placeholder')}
            />
          ) : (
            <span style={{ ...hintStyle(colors), marginTop: 0 }}>
              {allowedTools.length > 0 ? allowedTools.join(', ') : t('mcp.allowedTools.all')}
            </span>
          )}
        </Row>

        {useDecl && <div style={{ ...hintStyle(colors), marginTop: 10 }}>{t('mcp.decl.note')}</div>}
        <div style={{ ...hintStyle(colors), marginTop: 10 }}>{t('mcp.localNote')}</div>
      </Section>

      {/* Servers: read-only roster, or the editable draft. */}
      <Section
        title={t('mcp.servers.title', { n: editing ? draftServers.length : servers.length })}
        colors={colors}
        right={
          editing ? (
            <Pill onClick={addDraftServer} colors={colors} label={t('mcp.server.add')} accent small />
          ) : undefined
        }
      >
        {!editing && !online && <div style={hintStyle(colors)}>{t('mcp.servers.offline')}</div>}
        {!editing && online && loading && <div style={hintStyle(colors)}>{t('app.loading')}</div>}

        {!editing &&
          !(online && loading) &&
          servers.map((s) => {
            const statusText =
              s.status === 'connected'
                ? t('mcp.status.connected')
                : s.status === 'configured'
                  ? t('mcp.status.configured')
                  : s.status || ''
            const isOpen = openServer === s.name
            const tools = toolsByServer[s.name]
            return (
              <div
                key={s.name}
                style={{ border: '1px solid ' + colors.detailBorder, borderRadius: 6, padding: '10px 12px', marginBottom: 8, background: colors.bg }}
              >
                <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                  <strong style={{ color: colors.text, fontSize: fontSizes.md }}>{s.name}</strong>
                  {s.protocol && <Chip colors={colors}>{s.protocol}</Chip>}
                  {statusText && <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{statusText}</span>}
                  {typeof s.tools === 'number' && s.tools > 0 && (
                    <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>{t('mcp.toolCount', { n: s.tools })}</span>
                  )}
                  <span style={{ marginLeft: 'auto' }}>
                    <Action
                      colors={colors}
                      label={isOpen ? t('mcp.server.tools.hide') : t('mcp.server.tools')}
                      onClick={() => toggleTools(s.name)}
                      active={isOpen}
                    />
                  </span>
                </div>

                <div style={{ marginTop: 4, fontSize: fontSizes.xs, color: colors.textDim, fontFamily: monoFont, wordBreak: 'break-all' }}>
                  {s.url}
                </div>

                {isOpen && (
                  <div style={{ marginTop: 8, paddingTop: 8, borderTop: '1px solid ' + colors.detailBorder }}>
                    {toolsBusy === s.name ? (
                      <div style={hintStyle(colors)}>{t('mcp.tools.loading')}</div>
                    ) : toolsErr[s.name] ? (
                      <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{toolsErr[s.name]}</div>
                    ) : !tools || tools.length === 0 ? (
                      <div style={hintStyle(colors)}>{t('mcp.tools.none')}</div>
                    ) : (
                      tools.map((tool) => (
                        <div key={tool.name} style={{ marginBottom: 8 }}>
                          <div style={{ display: 'flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
                            <code style={{ color: colors.text, fontSize: fontSizes.sm, fontFamily: monoFont }}>{tool.name}</code>
                            {tool.description && (
                              <span style={{ color: colors.textDim, fontSize: fontSizes.xs, lineHeight: 1.5 }}>{tool.description}</span>
                            )}
                          </div>
                          {/* Both halves of the tool contract: the arguments
                              (schema) and, when the server declares one, the
                              structured result (response / outputSchema). */}
                          {!!tool.schema && (
                            <details style={{ marginTop: 2 }}>
                              <summary style={{ cursor: 'pointer', color: colors.textDimmed, fontSize: fontSizes.xs }}>
                                {t('mcp.tools.schema')}
                              </summary>
                              <pre style={preStyle(colors)}>{JSON.stringify(tool.schema, null, 2)}</pre>
                            </details>
                          )}
                          {!!tool.response && (
                            <details style={{ marginTop: 2 }}>
                              <summary style={{ cursor: 'pointer', color: colors.textDimmed, fontSize: fontSizes.xs }}>
                                {t('mcp.tools.response')}
                              </summary>
                              <pre style={preStyle(colors)}>{JSON.stringify(tool.response, null, 2)}</pre>
                            </details>
                          )}
                        </div>
                      ))
                    )}
                    <Action colors={colors} label={t('mcp.tools.refresh')} onClick={() => void loadTools(s.name)} />
                  </div>
                )}
              </div>
            )
          })}

        {!editing && !(online && loading) && servers.length === 0 && (
          <div style={hintStyle(colors)}>{t('mcp.servers.none')}</div>
        )}

        {/* Draft: one card per server, all values applied together on Save. */}
        {editing &&
          draftServers.map((r) => (
            <div
              key={r.key}
              style={{ border: '1px solid ' + colors.detailBorder, borderRadius: 6, padding: '10px 12px', marginBottom: 8, background: colors.bg }}
            >
              <Row style={{ marginBottom: 8 }}>
                <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed }}>
                  {r.orig ? t('mcp.edit.existing') : t('mcp.edit.new')}
                </span>
                <span style={{ marginLeft: 'auto' }}>
                  <Action colors={colors} label={t('mcp.server.remove')} danger onClick={() => setDraftServers((prev) => prev.filter((x) => x.key !== r.key))} />
                </span>
              </Row>

              <Field
                colors={colors}
                label={t('mcp.server.name')}
                hint={r.orig ? t('mcp.server.nameLocked') : undefined}
              >
                <input
                  style={{ ...inputStyle(colors), opacity: r.orig ? 0.7 : 1 }}
                  value={r.name}
                  disabled={!!r.orig}
                  onChange={(e) => patchDraft(r.key, { name: e.target.value })}
                  placeholder="github"
                />
              </Field>
              <Field colors={colors} label={t('mcp.server.url')}>
                <input
                  style={inputStyle(colors)}
                  value={r.url}
                  onChange={(e) => patchDraft(r.key, { url: e.target.value })}
                  placeholder="https://api.githubcopilot.com/mcp"
                />
              </Field>
              <Field colors={colors} label={t('mcp.server.protocol')}>
                <ProtocolSelect colors={colors} value={r.protocol} onChange={(v) => patchDraft(r.key, { protocol: v })} />
              </Field>
              <Field
                colors={colors}
                label={t('mcp.server.headers')}
                hint={useDecl ? t('mcp.server.headers.declHint') : t('mcp.server.headers.keepHint')}
              >
                <textarea
                  style={{ ...inputStyle(colors), minHeight: 48, resize: 'vertical', fontFamily: monoFont }}
                  value={r.headers}
                  onChange={(e) => patchDraft(r.key, { headers: e.target.value })}
                  placeholder={t('mcp.server.headers.placeholder')}
                />
              </Field>
            </div>
          ))}

        {editing && draftServers.length === 0 && <div style={hintStyle(colors)}>{t('mcp.servers.none')}</div>}
        {editing && <div style={{ ...hintStyle(colors), marginTop: 8 }}>{t('mcp.servers.editHint')}</div>}
      </Section>
    </div>
  )
}
