import { useEffect, useRef, useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { usePolling } from '../hooks/usePolling'
import JsonEditor from '../components/JsonEditor'
import BufferedInput, { envToText, textToMap } from '../components/BufferedInput'
import ViewHeader from '../components/ViewHeader'
import ResizablePanel from '../components/ResizablePanel'
import { CONTROL, fetchTemplates, fetchTemplate, createTemplateBody, updateTemplate, deleteTemplate, fetchProjects } from '../services/api'
import CreateTemplateDialog from './CreateTemplateDialog'
import CreateWorkerDialog from './CreateWorkerDialog'

const MOBILE_TOP_BAR_HEIGHT = 44

// TemplatesView manages the project template set, in the same shape as the
// workers view: a header with a create pill, a list of templates, and a
// right-anchored drawer (ResizablePanel) holding the template editor — a
// syntax-highlighted JSON editor over the whole template body. A template
// exported from a project opens as an editable draft and only reaches disk on
// save. All calls go to the control plane on :9527.
export default function TemplatesView({ isMobile }: { isMobile?: boolean }) {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  const [templates, setTemplates] = useState<string[]>([])
  const [projects, setProjects] = useState<string[]>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [error, setError] = useState('')

  usePolling<string[]>(CONTROL + '/api/templates', 5000, setTemplates, true)
  usePolling<string[]>(CONTROL + '/api/projects', 5000,
    (list) => setProjects(list.map((p: any) => p.id)), true)

  // ── drawer editor state ──
  // jsonText is what the user types; tmpl is its last valid parse and the
  // object saved to disk. The save button stays blocked while the text does
  // not parse.
  const [draftId, setDraftId] = useState('')
  const [isNew, setIsNew] = useState(false)
  const [tmpl, setTmpl] = useState<any>(null)
  const [jsonText, setJsonText] = useState('')
  const [jsonError, setJsonError] = useState('')
  const [saveError, setSaveError] = useState('')
  const [saved, setSaved] = useState(false)
  const [showAddWorker, setShowAddWorker] = useState(false)
  const [editorTab, setEditorTab] = useState<'workers' | 'json'>('workers')
  const [drawerWidth, setDrawerWidth] = useState(() =>
    Math.max(420, Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.4)))

  const openEditor = (body: any, name: string, draft: boolean) => {
    setDraftId(name)
    setIsNew(draft)
    setTmpl(body)
    setJsonText(JSON.stringify(body, null, 2))
    setJsonError('')
    setSaveError('')
    setSelected(name)
  }

  const openTemplate = (name: string) => {
    setError('')
    fetchTemplate(name)
      .then((body) => openEditor(body, name, false))
      .catch(() => setError(t('templates.error.open', { id: name })))
  }

  // closeDrawer clears the whole editor state — the drawer renders while tmpl
  // is set, so resetting only the selection would leave it open.
  const closeDrawer = () => {
    setSelected(null)
    setTmpl(null)
    setJsonText('')
    setJsonError('')
    setSaveError('')
    setSaved(false)
  }

  const onJsonChange = (text: string) => {
    setJsonText(text)
    setSaved(false)
    try {
      setTmpl(JSON.parse(text))
      setJsonError('')
    } catch {
      // Keep the last valid tmpl; save stays blocked while the text is invalid.
      setJsonError(t('templates.error.json'))
    }
  }

  // appendWorker adds a worker entry built by the create-worker dialog (same
  // form, no live worker is launched) and regenerates the JSON text.
  const appendWorker = (body: Record<string, unknown>) => {
    setShowAddWorker(false)
    if (!tmpl) return
    const next = { ...tmpl, workers: [...(tmpl.workers || []), body] }
    setTmpl(next)
    setJsonText(JSON.stringify(next, null, 2))
    setJsonError('')
    setSaved(false)
  }

  // Card edits regenerate the JSON text, so they are only safe while tmpl is
  // current — i.e. the JSON parses. While it does not, the cards pause (the
  // error note points at the JSON) instead of clobbering what is being typed.
  const updateWorker = (i: number, patch: any) => {
    if (!tmpl || jsonError) return
    const workers = tmpl.workers.map((w: any, j: number) => (j === i ? { ...w, ...patch } : w))
    setTmpl({ ...tmpl, workers })
    setJsonText(JSON.stringify({ ...tmpl, workers }, null, 2))
    setSaved(false)
  }

  const removeWorker = (i: number) => {
    if (!tmpl || jsonError) return
    const next = { ...tmpl, workers: tmpl.workers.filter((_: any, j: number) => j !== i) }
    setTmpl(next)
    setJsonText(JSON.stringify(next, null, 2))
    setSaved(false)
  }

  const save = async () => {
    if (!tmpl || jsonError) return
    const id = draftId.trim()
    if (!id) { setSaveError(t('templates.error.required')); return }
    setSaveError('')
    try {
      if (isNew) {
        await createTemplateBody(id, tmpl)
      } else {
        await updateTemplate(draftId, tmpl)
      }
      try { setTemplates(await fetchTemplates()) } catch { /* usePolling retries */ }
      // A saved draft becomes a saved template: the name locks and the next
      // save goes through PUT.
      setIsNew(false)
      setDraftId(id)
      setSaved(true)
      setSelected(id)
    } catch (e) {
      setSaved(false)
      setSaveError((e as Error)?.message || t('templates.error.save', { id }))
    }
  }

  const remove = async (name: string) => {
    setError('')
    try {
      await deleteTemplate(name)
      if (selected === name) closeDrawer()
    } catch (e) {
      setError(t('templates.error.delete', { id: name }))
    }
  }

  const createPill = (
    <span
      onClick={() => setShowCreate(true)}
      className="btn-hover"
      style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: isMobile ? '6px 14px' : '3px 10px', userSelect: 'none' }}
    >
      {t('templates.create')}
    </span>
  )

  const drawerBody = tmpl && (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', background: colors.bg, overflow: 'hidden' }}>
      {/* Header: editable name for drafts, fixed for saved templates; the
          close pill mirrors the worker detail's. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '12px 16px', borderBottom: '1px solid ' + colors.border, flexShrink: 0 }}>
        {isNew ? (
          <input
            value={draftId}
            onChange={(e) => { setDraftId(e.target.value); setSaved(false) }}
            placeholder={t('templates.newId.placeholder')}
            style={{ flex: 1, minWidth: 0, padding: '5px 8px', fontSize: fontSizes.md, background: colors.bgLight, color: colors.text, border: '1px solid ' + colors.border, borderRadius: 4, outline: 'none' }}
          />
        ) : (
          <span style={{ flex: 1, minWidth: 0, color: colors.text, fontSize: fontSizes.md, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{draftId}</span>
        )}
        <span
          onClick={closeDrawer}
          className="btn-hover"
          title={t('detail.close.tooltip')}
          style={{
            cursor: 'pointer',
            border: '1px solid ' + colors.border,
            borderRadius: 4,
            padding: '0 8px',
            color: colors.textDim,
            fontSize: fontSizes.md,
            lineHeight: '20px',
            userSelect: 'none',
            flexShrink: 0,
          }}
        >
          {'\u2715'}
        </span>
      </div>

      {/* Tabs: worker cards and the raw JSON editor are two views over the
          same template object — edits in either regenerate the other. Save
          lives on this row too, right-aligned. */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 8, padding: '12px 16px 14px', flexShrink: 0 }}>
        {(['workers', 'json'] as const).map((tab) => (
          <span
            key={tab}
            onClick={() => setEditorTab(tab)}
            style={{
              cursor: 'pointer',
              userSelect: 'none',
              border: '1px solid ' + (editorTab === tab ? colors.accent : colors.border),
              borderRadius: 2,
              padding: '3px 12px',
              fontSize: fontSizes.sm,
              color: editorTab === tab ? colors.accent : colors.textDim,
              background: editorTab === tab ? colors.bgChip : undefined,
            }}
          >
            {tab === 'workers' ? t('templates.tab.visual') : t('templates.tab.json')}
          </span>
        ))}
        <span style={{ flex: 1 }} />
        {saved && (
          <span style={{ color: colors.textDim, fontSize: fontSizes.sm }}>✓ {t('templates.saved')}</span>
        )}
        {!saved && saveError && (
          <span title={saveError} style={{ color: colors.toolFailed, fontSize: fontSizes.sm, maxWidth: '45%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{saveError}</span>
        )}
        <span
          onClick={save}
          className="btn-hover"
          style={{
            cursor: jsonError ? 'not-allowed' : 'pointer',
            border: '1px solid ' + (jsonError ? colors.border : colors.accent),
            borderRadius: 4,
            padding: '3px 12px',
            color: jsonError ? colors.textDimmed : colors.accent,
            fontSize: fontSizes.sm,
            userSelect: 'none',
            flexShrink: 0,
          }}
        >
          {t('templates.save')}
        </span>
      </div>

      {/* Body: the active tab's content. */}
      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: 16, display: 'flex', flexDirection: 'column', gap: 12 }}>
        {editorTab === 'workers' && (
          <>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <div style={{ color: colors.detailLabel, textTransform: 'uppercase', letterSpacing: '0.5px', fontSize: fontSizes.sm }}>{t('templates.workers')}</div>
              <span style={{ flex: 1 }} />
              <span
                onClick={() => setShowAddWorker(true)}
                className="btn-hover"
                style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '2px 10px', userSelect: 'none' }}
              >
                + {t('templates.addWorker')}
              </span>
            </div>
            {(tmpl.workers || []).length === 0 && (
              <div style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('templates.noWorkers')}</div>
            )}
            {(tmpl.workers || []).map((w: any, i: number) => {
          const cardLabel: React.CSSProperties = { display: 'block', fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4 }
          const field = (label: string, node: any, key?: string, style?: React.CSSProperties) => (
            <div key={key} style={style}>
              <label style={cardLabel}>{label}</label>
              {node}
            </div>
          )
          return (
          <div key={i + ':' + (w.id || '')} style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '12px 14px', display: 'flex', flexDirection: 'column', gap: 8, background: colors.detailBg }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
              <span style={{ color: colors.accent, fontSize: fontSizes.md }}>{w.type || '?'}</span>
              {w.managed === false && (
                <span style={{ color: colors.textDimmed, fontSize: fontSizes.xs, border: '1px solid ' + colors.border, borderRadius: 2, padding: '1px 6px' }}>{t('templates.external')}</span>
              )}
              <span style={{ flex: 1 }} />
              <button
                onClick={() => removeWorker(i)}
                style={{ cursor: 'pointer', background: 'transparent', color: colors.toolFailed, border: '1px solid ' + colors.border, borderRadius: 2, padding: '3px 10px', fontSize: fontSizes.sm }}
              >
                {t('templates.delete')}
              </button>
            </div>
            {field('id', (
              <input
                value={w.id || ''}
                onChange={(e) => updateWorker(i, { id: e.target.value })}
                style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors) }}
              />
            ))}
            {w.type === 'reason' && field(t('templates.field.instruction'), (
              <textarea
                value={w.instruction || ''}
                onChange={(e) => updateWorker(i, { instruction: e.target.value })}
                style={{ ...cardInput(colors), resize: 'vertical', minHeight: 56, width: '100%', boxSizing: 'border-box' }}
              />
            ))}
            {w.type === 'reason' && (
              <div style={{ display: 'flex', gap: 8 }}>
                {field(t('templates.field.provider'), (
                  <input value={w.provider || ''} onChange={(e) => updateWorker(i, { provider: e.target.value })} style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors) }} />
                ), 'p', { flex: 1, minWidth: 0 })}
                {field(t('templates.field.model'), (
                  <input value={w.model || ''} onChange={(e) => updateWorker(i, { model: e.target.value })} style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors) }} />
                ), 'm', { flex: 1, minWidth: 0 })}
              </div>
            )}            {(w.type === 'workspace' || w.type === 'program') && field(t('templates.field.mounts'), (
              <BufferedInput
                value={Array.isArray(w.mounts) ? w.mounts.join(', ') : ''}
                onText={(text) => {
                  const mounts = text.split(',').map((x: string) => x.trim()).filter(Boolean)
                  updateWorker(i, { mounts: mounts.length > 0 ? mounts : undefined })
                }}
                style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors) }}
              />
            ))}
            {w.managed === false && (
              <>
                {field(t('templates.field.command'), (
                  <BufferedInput
                    value={Array.isArray(w.command) ? w.command.join(' ') : ''}
                    onText={(text) => {
                      const command = text.split(/\s+/).filter(Boolean)
                      updateWorker(i, { command: command.length > 0 ? command : undefined })
                    }}
                    style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors) }}
                  />
                ))}
                {field(t('templates.field.cwd'), (
                  <input value={w.cwd || ''} onChange={(e) => updateWorker(i, { cwd: e.target.value })} style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors) }} />
                ))}
                {field(t('templates.field.env'), (
                  <BufferedInput
                    value={envToText(w.env)}
                    onText={(text) => {
                      const env = textToMap(text)
                      updateWorker(i, { env: Object.keys(env).length > 0 ? env : undefined })
                    }}
                    style={{ width: '100%', boxSizing: 'border-box', ...cardInput(colors), fontFamily: 'monospace', resize: 'vertical', minHeight: 48 }}
                    as="textarea"
                  />
                ))}
              </>
            )}
          </div>
          )
        })}
          </>
        )}

        {editorTab === 'json' && (
          <>
            <JsonEditor value={jsonText} onChange={onJsonChange} dark={dark} colors={colors} />
            {jsonError && <div style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{jsonError}</div>}
          </>
        )}
      </div>
    </div>
  )

  return (
    <div style={{ flex: 1, minWidth: 0, position: 'relative', display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {!isMobile ? (
        <ViewHeader
          title={t('templates.title')}
          count={templates.length}
          right={createPill}
        />
      ) : (
        <div style={{ display: 'flex', justifyContent: 'flex-end', padding: '12px 16px 8px' }}>
          {createPill}
        </div>
      )}

      {error && <div style={{ color: colors.toolFailed, padding: '0 24px 12px', fontSize: fontSizes.sm }}>{error}</div>}

      {/* Template list */}
      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: '12px 24px 24px' }}>
        {templates.length === 0 ? (
          <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>{t('templates.empty')}</div>
        ) : (
          templates.map((name) => (
            <div
              key={name}
              onClick={() => openTemplate(name)}
              style={{
                border: '1px solid ' + (selected === name ? colors.accent : colors.border),
                borderRadius: 6,
                padding: '10px 14px',
                marginBottom: 8,
                display: 'flex',
                alignItems: 'center',
                gap: 10,
                cursor: 'pointer',
              }}
            >
              <div style={{ flex: 1, color: selected === name ? colors.accent : colors.text, fontSize: fontSizes.md }}>{name}</div>
              <button
                onClick={(e) => { e.stopPropagation(); remove(name) }}
                style={{ cursor: 'pointer', background: 'transparent', color: colors.toolFailed, border: '1px solid ' + colors.border, borderRadius: 2, padding: '4px 10px', fontSize: fontSizes.sm }}
              >
                {t('templates.delete')}
              </button>
            </div>
          ))
        )}
      </div>

      {/* Drawer: template editor (draft or saved) */}
      {tmpl && (isMobile ? (
        <div style={{ position: 'fixed', top: MOBILE_TOP_BAR_HEIGHT, right: 0, bottom: 0, width: '100%', zIndex: 20, display: 'flex' }}>
          {drawerBody}
        </div>
      ) : (
        <ResizablePanel width={drawerWidth} minWidth={420} onWidthChange={setDrawerWidth}>
          {drawerBody}
        </ResizablePanel>
      ))}

      <CreateTemplateDialog
        open={showCreate}
        templates={templates}
        projects={projects}
        onClose={() => setShowCreate(false)}
        onCreated={async (id) => {
          setShowCreate(false)
          try { setTemplates(await fetchTemplates()) } catch { /* usePolling retries */ }
          setSelected(id)
        }}
        onDraft={(id, body) => {
          setShowCreate(false)
          openEditor(body, id, true)
        }}
      />

      {/* The workers' create dialog reused as a builder: same form, but submit
          appends the built config into the template's workers (no launch). */}
      <CreateWorkerDialog
        open={showAddWorker}
        onClose={() => setShowAddWorker(false)}
        onCreated={() => {}}
        onBuild={appendWorker}
      />
    </div>
  )
}

// cardInput is the drawer card's form-control style (the webui convention).
function cardInput(colors: any) {
  return {
    padding: '5px 8px',
    fontSize: fontSizes.sm,
    background: colors.bgLight,
    color: colors.text,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    outline: 'none',
  }
}

