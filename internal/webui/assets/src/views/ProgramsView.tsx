import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { usePolling } from '../hooks/usePolling'
import ViewHeader from '../components/ViewHeader'
import ResizablePanel from '../components/ResizablePanel'
import { fetchPrograms, fetchProgramDetail, updateProgram, deleteProgram } from '../services/api'
import type { ProgramDetail, ProgramInfo } from '../types'

const MOBILE_TOP_BAR_HEIGHT = 44

// ProgramsView is the WebUI's program browser. The left pane lists every
// program under the attached project (from the program worker's search); the
// right pane is the program detail panel — read once, mirrored from the worker
// detail panel's look — with edit (metadata + entry body) and delete. It needs
// a running project (and its program worker): without one (control mode) it
// shows a hint instead.
interface ProgramsViewProps {
  project?: string
  isMobile?: boolean
}

export default function ProgramsView({ project, isMobile }: ProgramsViewProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [programs, setPrograms] = useState<ProgramInfo[]>([])
  const [selected, setSelected] = useState<ProgramInfo | null>(null)
  const [drawerWidth, setDrawerWidth] = useState(() =>
    Math.max(440, Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.42)))

  // Poll the attached project for its programs. No project → no request.
  usePolling<ProgramInfo[]>(project ? '/api/programs' : '', 5000, setPrograms, !!project)

  const refresh = useCallback(async () => {
    if (!project) return
    try { setPrograms(await fetchPrograms()) } catch { /* next poll retries */ }
  }, [project])

  // On a project change, clear the (stale) selection.
  useEffect(() => { setSelected(null) }, [project])

  const onDeleted = useCallback(async () => {
    setSelected(null)
    if (project) { try { setPrograms(await fetchPrograms()) } catch { /* poll retries */ } }
  }, [project])

  return (
    <div style={{ flex: 1, minWidth: 0, position: 'relative', display: 'flex', overflow: 'hidden' }}>
      {/* Left: the program list. */}
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
        {!isMobile && <ViewHeader title={t('programs.title')} count={programs.length} />}
        <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: '12px 24px 24px' }}>
          {!project ? (
            <div style={{ color: colors.textDim, padding: '12px 24px 0', fontSize: fontSizes.md }}>
              {t('programs.noProject')}
            </div>
          ) : programs.length === 0 ? (
            <div style={{ color: colors.textDim, padding: '12px 24px 0', fontSize: fontSizes.md }}>
              {t('programs.empty')}
            </div>
          ) : (
            <table style={{ width: '100%', minWidth: 560, borderCollapse: 'collapse', fontSize: fontSizes.md }}>
              <thead>
                <tr style={{ textAlign: 'left', color: colors.textDimmed, fontSize: fontSizes.xs }}>
                  <th style={{ padding: '6px 12px' }}>{t('programs.col.name')}</th>
                  <th style={{ padding: '6px 12px' }}>{t('programs.col.form')}</th>
                  <th style={{ padding: '6px 12px' }}>{t('programs.col.contentType')}</th>
                  <th style={{ padding: '6px 12px' }}>{t('programs.col.description')}</th>
                  <th style={{ padding: '6px 12px' }}>{t('programs.col.tags')}</th>
                  <th style={{ padding: '6px 12px' }}>{t('programs.col.contents')}</th>
                </tr>
              </thead>
              <tbody>
                {programs.map((p) => (
                  <tr
                    key={p.name}
                    onClick={() => setSelected(p)}
                    style={{
                      borderBottom: '1px solid ' + colors.border,
                      cursor: 'pointer',
                      background: selected?.name === p.name ? colors.bgLight : undefined,
                    }}
                  >
                    <td style={{ padding: '8px 12px', color: p.locked ? colors.accent : colors.text, fontWeight: p.locked ? 'bold' : 'normal' }}>
                      {p.name}
                      {p.locked && <Chip label={t('programs.locked')} colors={colors} />}
                    </td>
                    <td style={{ padding: '8px 12px', color: colors.textDim }}>{formLabel(t, p.form_type)}</td>
                    <td style={{ padding: '8px 12px', color: colors.textDim }}>{contentTypeLabel(t, p.content_type)}</td>
                    <td style={{ padding: '8px 12px', color: colors.textMuted }}>{p.description}</td>
                    <td style={{ padding: '8px 12px' }}>
                      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                        {(p.tags || []).map((tag) => <Chip key={tag} label={tag} colors={colors} />)}
                      </div>
                    </td>
                    <td style={{ padding: '8px 12px', color: colors.textDim }}>
                      {p.contents !== undefined ? String(p.contents) : ''}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      </div>

      {/* Right: the program detail / editor drawer. */}
      {selected && (isMobile ? (
        <div style={{ position: 'fixed', top: MOBILE_TOP_BAR_HEIGHT, right: 0, bottom: 0, width: '100%', zIndex: 20, display: 'flex' }}>
          <ProgramDetailPanel name={selected.name} onClose={() => setSelected(null)} onDeleted={onDeleted} onSaved={refresh} />
        </div>
      ) : (
        <ResizablePanel width={drawerWidth} minWidth={360} onWidthChange={setDrawerWidth}>
          <ProgramDetailPanel name={selected.name} onClose={() => setSelected(null)} onDeleted={onDeleted} onSaved={refresh} />
        </ResizablePanel>
      ))}
    </div>
  )
}

// A tiny colored chip for tags / locked flags.
function Chip({ label, colors }: { label: string; colors: any }) {
  return (
    <span style={{
      display: 'inline-block', marginLeft: 6, padding: '1px 7px', borderRadius: 3,
      border: '1px solid ' + colors.border, color: colors.textDim, fontSize: fontSizes.xs,
    }}>
      {label}
    </span>
  )
}

function formLabel(t: (k: any) => string, f?: string): string {
  if (f === 'prompt') return t('programs.form.prompt')
  if (f === 'script') return t('programs.form.script')
  return f || '\u2014'
}

function contentTypeLabel(t: (k: any) => string, ct?: string): string {
  if (ct === 'instruction') return t('programs.contentType.instruction')
  if (ct === 'playbook') return t('programs.contentType.playbook')
  return ct || '\u2014'
}

// ── Detail panel ──

// ProgramDetailPanel is the program editor, styled after the worker detail
// panel: a sticky header (name + flags + close), a column of bg cards, an
// edit toggle that swaps read-only rows for editable inputs, and a delete
// action with confirm. All reads/writes go to the program worker over HTTP.
interface PanelProps {
  name: string
  onClose: () => void
  onDeleted: () => void
  onSaved: () => void
}

function ProgramDetailPanel({ name, onClose, onDeleted, onSaved }: PanelProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [prog, setProg] = useState<ProgramDetail | null>(null)
  const [loadErr, setLoadErr] = useState('')
  const [editing, setEditing] = useState(false)
  const [confirmDel, setConfirmDel] = useState(false)
  const [busy, setBusy] = useState('') // 'save' | 'delete'
  const [note, setNote] = useState('')

  // Draft edit fields, initialised from the loaded program when editing opens.
  const [draft, setDraft] = useState<{ contentType: string; description: string; tags: string; body: string }>({
    contentType: '', description: '', tags: '', body: '',
  })

  useEffect(() => {
    let alive = true
    setLoadErr('')
    fetchProgramDetail(name)
      .then((d) => { if (alive) setProg(d) })
      .catch(() => { if (alive) setLoadErr(t('programs.error.load')) })
    return () => { alive = false }
  }, [name, t])

  // When the loaded program changes (open / reload), seed the draft.
  useEffect(() => {
    if (prog) {
      setDraft({
        contentType: prog.content_type || '',
        description: prog.description || '',
        tags: (prog.tags || []).join(', '),
        body: prog.body || '',
      })
    }
  }, [prog])

  const beginEdit = () => { setEditing(true); setNote('') }
  const cancelEdit = () => { setEditing(false); setNote('') }

  const save = async () => {
    if (!prog || busy) return
    const tags = draft.tags.split(',').map((x: string) => x.trim()).filter(Boolean)
    setBusy('save'); setNote('')
    try {
      await updateProgram(name, {
        content_type: draft.contentType,
        description: draft.description,
        tags,
      }, draft.body)
      // Reload the freshly saved detail; then tell the list to refresh and
      // leave edit mode.
      const fresh = await fetchProgramDetail(name)
      setProg(fresh)
      setEditing(false)
      setNote(t('programs.saved'))
      onSaved()
    } catch (e) {
      setNote((e as Error)?.message || t('programs.error.save'))
    } finally {
      setBusy('')
    }
  }

  const doDelete = async () => {
    if (busy) return
    setBusy('delete'); setNote('')
    try {
      await deleteProgram(name)
      onDeleted()
    } catch (e) {
      setNote((e as Error)?.message || 'delete failed')
      setConfirmDel(false)
    } finally {
      setBusy('')
    }
  }

  const locked = !!prog?.locked

  const tag: React.CSSProperties = {
    display: 'inline-block', padding: '0 6px', borderRadius: 4, fontSize: fontSizes.sm,
    lineHeight: '18px', color: colors.textDim, background: colors.bgChip, border: '1px solid ' + colors.border,
  }

  return (
    <div style={{ flex: 1, width: '100%', minWidth: 0, overflowY: 'auto', fontSize: fontSizes.base, background: colors.bg }}>
      {/* Sticky header */}
      <div style={{
        display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap', position: 'sticky', top: 0, zIndex: 1,
        background: colors.bg, borderBottom: '1px solid ' + colors.border, padding: '12px 14px 8px',
      }}>
        <span style={{ color: locked ? colors.accent : colors.text, fontSize: fontSizes.md, fontWeight: locked ? 'bold' : 'normal' }}>{name}</span>
        {prog?.form_type && <span style={tag}>{formLabel(t, prog.form_type)}</span>}
        {locked && <span style={tag}>{t('programs.locked')}</span>}
        <span
          onClick={onClose}
          className="btn-hover"
          title={t('detail.close.tooltip')}
          style={{
            cursor: 'pointer', marginLeft: 'auto', border: '1px solid ' + colors.border, borderRadius: 4,
            padding: '0 8px', color: colors.textDim, fontSize: fontSizes.md, lineHeight: '20px', userSelect: 'none',
          }}
        >
          {'\u2715'}
        </span>
      </div>

      <div style={{ padding: 14 }}>
        {loadErr ? (
          <div style={{ color: colors.toolFailed, fontSize: fontSizes.md }}>{loadErr}</div>
        ) : !prog ? (
          <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>{t('app.loading')}</div>
        ) : (
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {/* Details card */}
            <div style={{ padding: '12px 14px', background: colors.detailBg, borderRadius: 6, fontSize: fontSizes.base }}>
              <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                {t('programs.section.details')}
                {!locked && (
                  <span
                    onClick={editing ? cancelEdit : beginEdit}
                    style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, textTransform: 'none', letterSpacing: 0, marginLeft: 10, userSelect: 'none' }}
                  >
                    {editing ? t('wd.close') : t('programs.edit')}
                  </span>
                )}
              </div>
              {editing ? (
                <EditFields draft={draft} setDraft={setDraft} colors={colors} t={t} />
              ) : (
                <div style={{ display: 'grid', gridTemplateColumns: '120px 1fr', gap: '5px 16px', alignItems: 'baseline' }}>
                  <DetailRow label={t('programs.field.contentType')} value={contentTypeLabel(t, prog.content_type)} colors={colors} />
                  <DetailRow label={t('programs.field.form')} value={formLabel(t, prog.form_type)} colors={colors} />
                  <DetailRow label={t('programs.field.description')} value={prog.description || '\u2014'} colors={colors} />
                  <DetailRow label={t('programs.field.tags')} value={(prog.tags || []).join(', ') || '\u2014'} colors={colors} />
                  <DetailRow label={t('programs.locked')} value={prog.locked ? t('wd.yes') : t('wd.no')} colors={colors} />
                  <DetailRow label={t('programs.field.body')} value={prog.body ? prog.body.length + ' chars' : '0 chars'} colors={colors} />
                </div>
              )}
            </div>

            {/* Entry body card: view or edit the entry content. */}
            <div style={{ padding: '12px 14px', background: colors.detailBg, borderRadius: 6 }}>
              <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                {t('programs.field.body')}
              </div>
              {editing ? (
                <textarea
                  value={draft.body}
                  onChange={(e) => setDraft({ ...draft, body: e.target.value })}
                  spellCheck={false}
                  style={{ width: '100%', boxSizing: 'border-box', minHeight: 180, resize: 'vertical', fontFamily: 'monospace', fontSize: fontSizes.sm, ...inputStyle(colors) }}
                />
              ) : (
                <pre style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word', margin: 0, color: colors.textDim, fontSize: fontSizes.sm, fontFamily: 'inherit', lineHeight: 1.6 }}>
                  {prog.body || '\u2014'}
                </pre>
              )}
            </div>

            {/* Sub-contents card — a collapsible tree of the program's files. */}
            <div style={{ padding: '12px 14px', background: colors.detailBg, borderRadius: 6 }}>
              <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 8, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                {t('programs.subContents')}
              </div>
              <ContentTree name={name} paths={prog.contents || []} colors={colors} emptyLabel={t('programs.noSubContents')} />
            </div>

            {/* Actions card */}
            <div style={{ padding: '12px 14px', background: colors.detailBg, borderRadius: 6 }}>
              <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 10, textTransform: 'uppercase', letterSpacing: '0.5px' }}>
                {t('wd.actions')}
              </div>
              {locked ? (
                <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, lineHeight: 1.5 }}>{t('programs.lockedHint')}</div>
              ) : (
                <div>
                  {editing && (
                    <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap', marginBottom: 16 }}>
                      <span
                        onClick={save}
                        className="btn-hover"
                        style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '3px 12px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
                      >
                        {busy === 'save' ? t('programs.saving') : t('programs.save')}
                      </span>
                      <span
                        onClick={cancelEdit}
                        className="btn-hover"
                        style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
                      >
                        {t('wd.close')}
                      </span>
                      <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>{t('programs.saveHint')}</span>
                    </div>
                  )}

                  <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginBottom: 6, lineHeight: 1.5 }}>
                    {t('programs.delete.desc')}
                  </div>
                  {confirmDel ? (
                    <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
                      <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>{t('programs.delete.confirm', { name })}</span>
                      <span
                        onClick={doDelete}
                        className="btn-hover"
                        style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, border: '1px solid ' + colors.toolFailed, borderRadius: 4, padding: '3px 12px', color: colors.toolFailed, fontSize: fontSizes.sm, userSelect: 'none' }}
                      >
                        {busy === 'delete' ? t('programs.deleting') : t('programs.confirmDelete')}
                      </span>
                      <span
                        onClick={() => setConfirmDel(false)}
                        className="btn-hover"
                        style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
                      >
                        {t('wd.cancel')}
                      </span>
                    </div>
                  ) : (
                    <span
                      onClick={() => { setConfirmDel(true); setNote('') }}
                      className="btn-hover"
                      style={{ cursor: 'pointer', display: 'inline-block', border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 12px', color: colors.toolFailed, fontSize: fontSizes.md, userSelect: 'none' }}
                    >
                      {t('programs.delete')}
                    </span>
                  )}
                </div>
              )}
              {note && (
                <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, marginTop: 10, lineHeight: 1.5 }}>{note}</div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

// EditFields renders the editable metadata inputs (swap-in for the detail grid).
function EditFields({ draft, setDraft, colors, t }: {
  draft: { contentType: string; description: string; tags: string; body: string }
  setDraft: (d: { contentType: string; description: string; tags: string; body: string }) => void
  colors: any
  t: (k: any) => string
}) {
  const fieldLabel: React.CSSProperties = { display: 'block', fontSize: fontSizes.xs, color: colors.textDimmed, marginBottom: 4 }
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div>
        <label style={fieldLabel}>{t('programs.field.contentType')}</label>
        <select
          value={draft.contentType}
          onChange={(e) => setDraft({ ...draft, contentType: e.target.value })}
          style={{ ...inputStyle(colors), width: '100%', boxSizing: 'border-box' }}
        >
          <option value="">—</option>
          <option value="instruction">{t('programs.contentType.instruction')}</option>
          <option value="playbook">{t('programs.contentType.playbook')}</option>
        </select>
      </div>
      <div>
        <label style={fieldLabel}>{t('programs.field.description')}</label>
        <input
          value={draft.description}
          onChange={(e) => setDraft({ ...draft, description: e.target.value })}
          style={{ ...inputStyle(colors), width: '100%', boxSizing: 'border-box' }}
        />
      </div>
      <div>
        <label style={fieldLabel}>{t('programs.field.tags')}</label>
        <input
          value={draft.tags}
          placeholder={t('programs.tagsPlaceholder')}
          onChange={(e) => setDraft({ ...draft, tags: e.target.value })}
          style={{ ...inputStyle(colors), width: '100%', boxSizing: 'border-box' }}
        />
      </div>
    </div>
  )
}

function inputStyle(colors: any): React.CSSProperties {
  return {
    padding: '5px 8px', fontSize: fontSizes.sm, background: colors.bgLight, color: colors.text,
    border: '1px solid ' + colors.border, borderRadius: 4, outline: 'none',
  }
}

// DetailRow is the label/value row in the details grid (matches the worker
// detail's grid).
function DetailRow({ label, value, colors }: { label: string; value: string; colors: any }) {
  return (
    <>
      <span style={{ color: colors.detailLabel, fontSize: fontSizes.base }}>{label}</span>
      <span style={{ color: colors.textDimmed, fontSize: fontSizes.base, wordBreak: 'break-all' }}>{value}</span>
    </>
  )
}

// ── Sub-content tree ──

// TreeNode is one entry in the program's content tree: either a directory
// (children, no path) or a file (path, no children). path is a file's full
// abstract address ("{program}/dir/file.md").
interface TreeNode {
  name: string
  path?: string
  children?: TreeNode[]
}

// buildTree turns the flat content paths ("{program}/rules/go.md") into a
// nested directory/file tree rooted under the program.
function buildTree(name: string, paths: string[]): TreeNode[] {
  const roots: TreeNode[] = []
  for (const full of paths) {
    const rel = full.replace(new RegExp('^' + name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '/+'), '')
    const segments = rel.split('/').filter(Boolean)
    let level = roots
    let acc = name
    segments.forEach((seg, i) => {
      const isFile = i === segments.length - 1
      acc += '/' + seg
      let node = level.find((n) => n.name === seg)
      if (!node) {
        node = isFile ? { name: seg, path: acc } : { name: seg, children: [] }
        level.push(node)
      }
      if (!isFile && node.children) level = node.children
    })
  }
  return roots
}

// ContentTree renders a program's sub-content paths as a collapsible tree.
function ContentTree({ name, paths, colors, emptyLabel }: {
  name: string
  paths: string[]
  colors: any
  emptyLabel: string
}) {
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())
  const roots = useMemo(() => buildTree(name, paths), [name, paths])

  const toggle = (key: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  const renderNode = (node: TreeNode, key: string, depth: number): ReactNode => {
    const isDir = !!node.children && node.children.length > 0
    const expanded = !collapsed.has(key)
    const arrow = isDir ? (expanded ? '\u25BE' : '\u25B8') : null
    return (
      <div key={key}>
        <div
          onClick={isDir ? () => toggle(key) : undefined}
          style={{
            display: 'flex', alignItems: 'center', gap: 5, cursor: isDir ? 'pointer' : 'default', userSelect: 'none',
            padding: '2px 0', paddingLeft: depth * 16,
          }}
        >
          <span style={{ width: 10, flexShrink: 0, color: colors.textDimmed, fontSize: fontSizes.xs, textAlign: 'center' }}>{arrow}</span>
          <span style={{ color: isDir ? colors.textDim : colors.textMuted, fontSize: fontSizes.sm, wordBreak: 'break-all' }}>{node.name}</span>
        </div>
        {isDir && expanded && (node.children || []).map((c) => renderNode(c, key + '/' + c.name, depth + 1))}
      </div>
    )
  }

  if (paths.length === 0) {
    return <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed, lineHeight: 1.5 }}>{emptyLabel}</div>
  }
  return <div>{roots.map((r) => renderNode(r, name + '/' + r.name, 0))}</div>
}