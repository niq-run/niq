import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { createTemplate, fetchTemplatePreview } from '../services/api'

interface CreateTemplateDialogProps {
  open: boolean
  templates: string[]
  projects: string[]
  onClose: () => void
  // From a template: the clone lands on disk immediately.
  onCreated: (id: string) => void
  // From a project: the export opens as an editable draft in the drawer.
  // fromProject/includeProgram ride along and take effect when the draft is
  // saved (the programs/ resources are copied at that point).
  onDraft: (id: string, body: any, opts: { fromProject: string; includeProgram: boolean }) => void
}

// CreateTemplateDialog is the templates-view "new template" form, styled after
// the workers' create dialog: a centered overlay. The source is either another
// template (a whole-directory clone, immediate) or an existing project (its
// worker configurations open as an editable draft in the drawer, optionally
// carrying the project's programs).
export default function CreateTemplateDialog({ open, templates, projects, onClose, onCreated, onDraft }: CreateTemplateDialogProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [source, setSource] = useState<'template' | 'project'>('template')
  const [id, setId] = useState('')
  const [copyFrom, setCopyFrom] = useState('')
  const [fromProject, setFromProject] = useState('')
  const [includeProgram, setIncludeProgram] = useState(false)
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

  const submit = async () => {
    if (busy) return
    setNote('')
    const trimmed = id.trim()
    if (!/^[A-Za-z0-9._-]+$/.test(trimmed)) { setNote(t('templates.error.required')); return }
    const src = source === 'template' ? copyFrom : fromProject
    if (!src) { setNote(t('templates.error.required')); return }

    setBusy(true)
    try {
      if (source === 'template') {
        await createTemplate(trimmed, src)
        onCreated(trimmed)
      } else {
        const preview = await fetchTemplatePreview(src, includeProgram)
        onDraft(trimmed, preview, { fromProject: src, includeProgram })
      }
    } catch (e) {
      setNote(t('templates.error.create', { id: trimmed }))
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
          <span style={{ color: colors.text, fontSize: fontSizes.md }}>{t('templates.create.title')}</span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{ cursor: 'pointer', marginLeft: 'auto', border: '1px solid ' + colors.border, borderRadius: 2, padding: '0 8px', color: colors.textDim, fontSize: fontSizes.sm, lineHeight: '20px', userSelect: 'none' }}
          >
            {'\u2715'}
          </span>
        </div>

        {/* Source: another template (immediate clone) or a project (draft). */}
        <div style={{ display: 'flex', gap: 8, marginBottom: 8 }}>
          <span onClick={() => { setSource('template'); setNote('') }} style={pill(source === 'template')}>
            {t('templates.create.sourceTemplate')}
          </span>
          <span onClick={() => { setSource('project'); setNote('') }} style={pill(source === 'project')}>
            {t('templates.create.sourceProject')}
          </span>
        </div>
        <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, lineHeight: 1.5, marginBottom: 12 }}>
          {source === 'template' ? t('templates.create.sourceTemplateHint') : t('templates.create.sourceProjectHint')}
        </div>

        <div style={field}>
          <label style={label}>{t('templates.create.id')}</label>
          <input style={input} value={id} onChange={e => setId(e.target.value)} placeholder={t('templates.newId.placeholder')} />
        </div>
        <div style={field}>
          <label style={label}>{source === 'template' ? t('templates.templatePlaceholder') : t('templates.projectPlaceholder')}</label>
          {source === 'template' ? (
            <select style={input} value={copyFrom} onChange={e => setCopyFrom(e.target.value)}>
              <option value=""></option>
              {templates.map(x => <option key={x} value={x}>{x}</option>)}
            </select>
          ) : (
            <select style={input} value={fromProject} onChange={e => setFromProject(e.target.value)}>
              <option value=""></option>
              {projects.map(x => <option key={x} value={x}>{x}</option>)}
            </select>
          )}
        </div>

        {/* From a project only: carry the project's programs into the template. */}
        {source === 'project' && (
          <label style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 10, cursor: 'pointer', userSelect: 'none' }}>
            <input
              type="checkbox"
              checked={includeProgram}
              onChange={e => setIncludeProgram(e.target.checked)}
              style={{ margin: 0, accentColor: colors.accent }}
            />
            <span style={{ fontSize: fontSizes.sm, color: colors.textDim }}>{t('templates.create.includeProgram')}</span>
          </label>
        )}

        {note && (
          <div style={{ fontSize: fontSizes.sm, color: colors.toolFailed, marginBottom: 10, lineHeight: 1.5, wordBreak: 'break-all' }}>{note}</div>
        )}

        <div style={{ display: 'flex', gap: 8 }}>
          <span
            onClick={submit}
            className="btn-hover"
            style={{ cursor: busy ? 'default' : 'pointer', opacity: busy ? 0.6 : 1, border: '1px solid ' + colors.accent, borderRadius: 4, padding: '4px 14px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {busy ? t('templates.create.saving') : t('templates.create.submit')}
          </span>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{ cursor: 'pointer', border: '1px solid ' + colors.border, borderRadius: 2, padding: '4px 14px', color: colors.textDim, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {t('wd.cancel')}
          </span>
        </div>
      </div>
    </>
  )
}
