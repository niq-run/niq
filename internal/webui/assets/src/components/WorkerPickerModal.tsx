import { useState, useMemo, useRef } from 'react'
import { createPortal } from 'react-dom'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo } from '../types'
import TagFilterDropdown from './TagFilterDropdown'

interface WorkerPickerModalProps {
  title: string
  // Selectable workers, already filtered for the calling view (e.g. reason
  // workers only for the talk view), excluding archived ones.
  workers: WorkerInfo[]
  selected: Set<string>
  onToggle: (id: string) => void
  onClose: () => void
  isMobile: boolean
}

// A row in the modal's grouped list.
interface Row { kind: 'group' | 'worker'; key: string; group: string; indent?: number; w?: WorkerInfo }

// WorkerPickerModal is the expanded worker selector: a centered dialog with a
// search box, tag-grouped rows (slash tags nest by depth) and per-row toggle.
// It is where you pick when the sidebar's inline checklist gets crowded.
export default function WorkerPickerModal({ title, workers, selected, onToggle, onClose, isMobile }: WorkerPickerModalProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [query, setQuery] = useState('')
  const [selectedTags, setSelectedTags] = useState<string[]>([])
  // '' = all, 'online' / 'offline' filter by bus connection.
  const [onlineFilter, setOnlineFilter] = useState<'' | 'online' | 'offline'>('')
  const inputRef = useRef<HTMLInputElement>(null)

  // All distinct tags present across the selectable set (stable options for
  // the multi-select tag filter, independent of the current search query).
  const availableTags = useMemo(() => {
    const set = new Set<string>()
    for (const w of workers) for (const tag of w.tags || []) set.add(tag)
    return [...set].sort()
  }, [workers])

  const toggleTag = (tag: string) => {
    setSelectedTags(prev => prev.includes(tag) ? prev.filter(t => t !== tag) : [...prev, tag])
  }

  // Multi-select tag filter (OR: a worker is kept when it carries any selected
  // tag), then the free-text search on top of it.
  const tagMatched = selectedTags.length === 0
    ? workers
    : workers.filter(w => (w.tags || []).some(tag => selectedTags.includes(tag)))
  // Online-status filter ('' = all). A worker is offline only when online===false.
  const onlineMatched = onlineFilter === ''
    ? tagMatched
    : tagMatched.filter(w => onlineFilter === 'online' ? w.online !== false : w.online === false)
  const q = query.trim().toLowerCase()
  const filtered = q
    ? onlineMatched.filter(w =>
        w.id.toLowerCase().includes(q) ||
        (w.type || '').toLowerCase().includes(q) ||
        (w.description || '').toLowerCase().includes(q) ||
        (w.tags || []).some(tag => tag.toLowerCase().includes(q)))
    : onlineMatched

  // Build the grouped list: group header per primary-tag top segment, worker
  // rows indented by the tag's remaining depth; untagged workers fall into an
  // "other" group.
  const rows = useMemo((): Row[] => {
    const out: Row[] = []
    let lastGroup: string | null = null
    const emit = (g: string) => {
      if (g !== lastGroup) {
        out.push({ kind: 'group', key: '__grp_' + g, group: g })
        lastGroup = g
      }
    }
    const tagged = filtered.filter(w => (w.tags?.length ?? 0) > 0)
      .slice().sort((a, b) => a.tags![0].localeCompare(b.tags![0]))
    const untagged = filtered.filter(w => !w.tags?.length)
    for (const w of tagged) {
      const segs = w.tags![0].split('/')
      emit(segs[0])
      out.push({ kind: 'worker', key: w.id, group: w.tags![0], indent: Math.min(segs.length - 1, 3), w })
    }
    if (untagged.length) {
      emit(t('picker.group.untagged'))
      for (const w of untagged) {
        out.push({ kind: 'worker', key: w.id, group: t('picker.group.untagged'), w })
      }
    }
    return out
  }, [filtered, t])

  const checkedCount = workers.filter(w => selected.has(w.id)).length

  const input: React.CSSProperties = {
    width: '100%',
    boxSizing: 'border-box',
    background: colors.bg,
    border: '1px solid ' + colors.border,
    borderRadius: 4,
    padding: '6px 9px',
    color: colors.text,
    fontSize: fontSizes.base,
    outline: 'none',
  }

  // Rendered through a portal to document.body: the sidebar drawer applies a
  // CSS transform on mobile, which would otherwise re-anchor position:fixed
  // children to the drawer instead of the viewport.
  return createPortal(
    <>
      {/* Backdrop */}
      <div onClick={onClose} style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)', zIndex: 200 }} />
      <div
        onClick={e => e.stopPropagation()}
        style={{
          position: 'fixed',
          top: '50%',
          left: '50%',
          transform: 'translate(-50%, -50%)',
          width: 'min(560px, calc(100vw - 32px))',
          maxHeight: '82vh',
          display: 'flex',
          flexDirection: 'column',
          background: colors.bgLight,
          border: '1px solid ' + colors.border,
          borderRadius: 8,
          boxShadow: '0 8px 24px rgba(0,0,0,0.22)',
          zIndex: 201,
        }}
      >
        {/* Header: title + search + close */}
        <div style={{ padding: '12px 16px', borderBottom: '1px solid ' + colors.border }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 10 }}>
            <span style={{ color: colors.text, fontSize: fontSizes.md, fontWeight: 600 }}>{title}</span>
            <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm, flexShrink: 0 }}>
              {checkedCount}/{workers.length}
            </span>
            <span
              onClick={onClose}
              className="btn-hover"
              style={{ cursor: 'pointer', marginLeft: 'auto', border: '1px solid ' + colors.border, borderRadius: 2, padding: '0 8px', color: colors.textDim, fontSize: fontSizes.sm, lineHeight: '20px', userSelect: 'none' }}
            >
              {'\u2715'}
            </span>
          </div>
          <input
            ref={inputRef}
            autoFocus={!isMobile}
            value={query}
            onChange={e => setQuery(e.target.value)}
            placeholder={t('workerPicker.search')}
            style={input}
          />
          {/* Multi-select tag filter (shared dropdown component) on its own
              full-width row, sized to match the search input above so the two
              read as peers. Always shown — the dropdown reveals a "no tags"
              state when there is nothing to filter by. A worker is kept when
              it carries any of the selected tags. */}
          <div style={{ marginTop: 8 }}>
            <TagFilterDropdown
              fill
              variant="input"
              tags={availableTags}
              selected={selectedTags}
              onToggle={toggleTag}
              onClear={() => setSelectedTags([])}
            />
          </div>
          {/* Third row: online-status filter, full width, each option 1/3. */}
          <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
            {([['', t('workerPicker.online.all')], ['online', t('worker.online')], ['offline', t('worker.offline')]] as ['' | 'online' | 'offline', string][]).map(([val, label]) => {
              const on = onlineFilter === val
              return (
                <span
                  key={val || 'all'}
                  onClick={() => setOnlineFilter(val)}
                  className="btn-hover"
                  style={{
                    flex: '1 1 0',
                    cursor: 'pointer', userSelect: 'none', textAlign: 'center',
                    fontSize: fontSizes.sm, lineHeight: '28px', whiteSpace: 'nowrap',
                    color: on ? colors.accent : colors.textDim,
                    border: '1px solid ' + (on ? colors.accent : colors.border),
                    borderRadius: 4,
                    background: on ? colors.bgChip : undefined,
                  }}
                >
                  {label}
                </span>
              )
            })}
          </div>
        </div>

        {/* Body: grouped scrollable list */}
        <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', padding: '6px 0 8px' }}>
          {rows.length === 0 && (
            <div style={{ padding: '24px 16px', textAlign: 'center', color: colors.textDimmed, fontSize: fontSizes.sm }}>
              {t('workerPicker.empty')}
            </div>
          )}
          {rows.map(row => {
            if (row.kind === 'group') {
              return (
                <div
                  key={row.key}
                  style={{ padding: '10px 16px 3px 16px', fontSize: fontSizes.xs, color: colors.textDimmed, fontWeight: 600, letterSpacing: '0.03em', textTransform: 'uppercase', userSelect: 'none' }}
                >
                  {row.group}
                </div>
              )
            }
            const w = row.w!
            const isActive = selected.has(w.id)
            const padLeft = 16 + (row.indent ?? 0) * 14
            return (
              <div
                key={row.key}
                onClick={() => onToggle(w.id)}
                style={{
                  cursor: 'pointer',
                  userSelect: 'none',
                  display: 'flex',
                  alignItems: 'center',
                  gap: 8,
                  padding: `7px ${14}px 7px ${padLeft}px`,
                  background: isActive ? colors.bgChip : undefined,
                }}
              >
                <span
                  style={{
                    width: 13,
                    height: 13,
                    flexShrink: 0,
                    display: 'inline-flex',
                    alignItems: 'center',
                    justifyContent: 'center',
                    borderRadius: 3,
                    border: '1px solid ' + (isActive ? colors.accent : colors.border),
                    background: isActive ? colors.accent : 'transparent',
                    color: '#fff',
                    fontSize: 10,
                    lineHeight: 1,
                  }}
                >
                  {isActive ? '✓' : ''}
                </span>
                <span style={{ display: 'flex', flexDirection: 'column', gap: 2, flex: 1, minWidth: 0 }}>
                  {/* Line 1: worker name, tags right after it (no wrap). */}
                  <span style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
                    <span style={{ color: isActive ? colors.accent : colors.text, fontSize: fontSizes.base, fontWeight: isActive ? 600 : 400, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', flex: '0 1 auto', minWidth: 0 }}>
                      {w.id}
                    </span>
                    {(w.tags || []).map(tag => (
                      <span key={tag} style={{ fontSize: fontSizes.xs, color: colors.textDim, background: colors.bgLight, border: '1px solid ' + colors.border, borderRadius: 8, padding: '0 6px', lineHeight: '15px', whiteSpace: 'nowrap', flexShrink: 0 }}>{'#' + tag}</span>
                    ))}
                  </span>
                  {/* Line 2: one-line description. */}
                  {w.description && (
                    <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', lineHeight: 1.4 }}>{w.description}</span>
                  )}
                </span>
                <span style={{ flexShrink: 0, color: w.online === false ? colors.textDimmed : colors.toolCompleted, fontSize: fontSizes.sm }}>
                  {w.online === false ? t('worker.offline') : t('worker.online')}
                </span>
              </div>
            )
          })}
        </div>

        {/* Footer: close */}
        <div style={{ padding: '10px 16px', borderTop: '1px solid ' + colors.border, display: 'flex', justifyContent: 'flex-end' }}>
          <span
            onClick={onClose}
            className="btn-hover"
            style={{ cursor: 'pointer', border: '1px solid ' + colors.accent, borderRadius: 4, padding: '4px 16px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
          >
            {t('wd.close')}
          </span>
        </div>
      </div>
    </>,
    document.body,
  )
}