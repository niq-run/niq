import { useMemo, useState } from 'react'
import { useTheme, fontSizes, type Palette } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'
import SelectDropdown from '../components/SelectDropdown'
import CreateWorkerDialog from './CreateWorkerDialog'
import ViewHeader from '../components/ViewHeader'

interface WorkersViewProps {
  workers: WorkerInfo[]
  selectedId: string | null
  onSelect: (id: string) => void
  onOpenEvents: (id: string) => void
  archived: Set<string>
  isMobile: boolean
  onRefresh?: () => void
  busPort?: number
}

export default function WorkersView({ workers, selectedId, onSelect, onOpenEvents, archived, isMobile, onRefresh, busPort }: WorkersViewProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [showCreate, setShowCreate] = useState(false)

  // ── Search + filter (mirrors the sidebar's worker picker modal): free-text
  // search, a multi-select tag filter and an online/offline status filter. The
  // filtered set drives the table below.
  // ── Filter: free-text search plus three dropdowns (type, tag, online status).
  const [search, setSearch] = useState('')
  const [typeFilter, setTypeFilter] = useState('')
  const [tagFilter, setTagFilter] = useState('')
  // Default to ONLINE: the worker list is an at-a-glance roster of who is
  // live right now; offline declarations are one dropdown step away.
  const [onlineFilter, setOnlineFilter] = useState<'' | 'online' | 'offline'>('online')

  const availableTypes = useMemo(() => {
    const set = new Set<string>()
    for (const w of workers) if (w.type) set.add(w.type)
    return [...set].sort()
  }, [workers])

  const availableTags = useMemo(() => {
    const set = new Set<string>()
    for (const w of workers) for (const tag of w.tags || []) set.add(tag)
    return [...set].sort()
  }, [workers])

  // Type / tag / online-status filters combine with AND; the free-text query
  // matches the id, type, description or any tag.
  const query = search.trim().toLowerCase()
  const filtered = workers.filter(w =>
    (typeFilter === '' || w.type === typeFilter) &&
    (tagFilter === '' || (w.tags || []).includes(tagFilter)) &&
    (onlineFilter === '' || (onlineFilter === 'online' ? w.online !== false : w.online === false)) &&
    (query === '' || [w.id, w.label, w.type, w.description, ...(w.tags || [])]
      .some(v => (v || '').toLowerCase().includes(query)))
  )

  // Single-line cells with ellipsis (same as the events table), with a taller
  // row: the worker list is less dense than the event stream. Mobile rows are
  // taller still so they are easy to tap.
  const cell: React.CSSProperties = {
    padding: isMobile ? '16px 6px' : '10px 6px',
    verticalAlign: 'top',
    borderBottom: '1px solid ' + colors.eventRowBorder,
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 0,
  }

  // Tag badge, matching the events table's type tag but in a neutral tone.
  // Colours come from full-hex theme tokens: appending an alpha pair to a
  // 3-digit colour (textDimmed is #666) produces a 7-char value the browser
  // silently drops, leaving the badge with no background or border.
  const tag: React.CSSProperties = {
    display: 'inline-block',
    padding: '0 6px',
    borderRadius: 4,
    fontSize: fontSizes.sm,
    lineHeight: '18px',
    color: colors.textDim,
    background: colors.bgLight,
    border: '1px solid ' + colors.detailBorder,
    whiteSpace: 'nowrap',
    // A shared min-width keeps every type badge the same width regardless of
    // the label's length; a wider label still grows as needed.
    minWidth: 88,
    textAlign: 'center',
  }

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {!isMobile && (
        <ViewHeader
          title={t('workers.title')}
          count={workers.length}
          right={<>
            <span
              onClick={() => setShowCreate(true)}
              className="btn-hover"
              style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '3px 10px', userSelect: 'none' }}
            >
              {t('workers.create')}
            </span>
            {onRefresh && (
              <span
                onClick={onRefresh}
                className="btn-hover"
                title={t('workers.refresh.tooltip')}
                style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 2, padding: '3px 10px', userSelect: 'none' }}
              >
                {t('workers.refresh')}
              </span>
            )}
          </>}
        />
      )}
      {/* Mobile: the app-level top bar heads the page; the create action pill
          lives in a fixed bar above the scrollable table so it stays on-screen
          (it used to live inside the scroller and scrolled away). */}
      {isMobile && (
        <div style={{ display: 'flex', padding: '12px 24px 8px', flexShrink: 0 }}>
          <span
            onClick={() => setShowCreate(true)}
            className="btn-hover"
            style={{ flex: 1, textAlign: 'center', cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '10px 14px', userSelect: 'none' }}
          >
            {t('workers.create')}
          </span>
        </div>
      )}
      {/* Filter toolbar: three dropdowns (type / tag / online status).
          Renders above the table on desktop and mobile and stays put while
          the table scrolls. */}
      {workers.length > 0 && (
        <div style={{ flexShrink: 0, display: 'flex', gap: 16, padding: isMobile ? '4px 24px 8px' : '10px 24px 8px', flexWrap: 'wrap', alignItems: 'center', borderBottom: '1px solid ' + colors.border }}>
          <input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('worker.filter.search')}
            style={{ flex: '1 1 200px', minWidth: 160, maxWidth: 320, boxSizing: 'border-box', background: colors.bg, border: '1px solid ' + colors.border, borderRadius: 4, padding: '4px 9px', color: colors.text, fontSize: fontSizes.sm, outline: 'none' }}
          />
          <FilterField label={t('worker.filter.type')} colors={colors}>
            <SelectDropdown value={typeFilter} options={availableTypes} placeholder={t('worker.filter.all')} onChange={setTypeFilter} />
          </FilterField>
          <FilterField label={t('worker.filter.tag')} colors={colors}>
            <SelectDropdown value={tagFilter} options={availableTags} placeholder={t('worker.filter.all')} onChange={setTagFilter} render={(o) => '#' + o} />
          </FilterField>
          <FilterField label={t('worker.filter.status')} colors={colors}>
            <SelectDropdown
              value={onlineFilter}
              options={['online', 'offline']}
              placeholder={t('worker.filter.all')}
              onChange={(v) => setOnlineFilter(v as '' | 'online' | 'offline')}
              render={(o) => (o === 'online' ? t('worker.online') : t('worker.offline'))}
            />
          </FilterField>
        </div>
      )}
      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: '0 24px 16px' }}>

      {/* min-width lets the fixed columns scroll horizontally on narrow
          (phone) viewports instead of collapsing. */}
      {filtered.length === 0 ? (
        <div style={{ padding: '32px 16px', textAlign: 'center', color: colors.textDimmed, fontSize: fontSizes.sm }}>
          {t('workerPicker.empty')}
        </div>
      ) : (
      <table style={{ width: '100%', minWidth: 780, borderCollapse: 'separate', borderSpacing: 0, tableLayout: 'fixed', fontSize: fontSizes.md }}>
        <thead>
          <tr style={{ textAlign: 'left', color: colors.textDimmed, fontSize: fontSizes.xs }}>
            <th style={{ padding: '6px 6px', width: 160, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }}>{t('workers.col.workerId')}</th>
            <th style={{ padding: '6px 6px', width: 140, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.label.tooltip')}>{t('workers.col.label')}</th>
            <th style={{ padding: '6px 6px', width: 110, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }}>{t('workers.col.type')}</th>
            <th style={{ padding: '6px 6px', width: 200, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.description.tooltip')}>{t('workers.col.description')}</th>
            <th style={{ padding: '6px 6px', width: 90, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.connection.tooltip')}>{t('workers.col.connection')}</th>
            <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.lifecycle.tooltip')}>{t('workers.col.lifecycle')}</th>
            <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.managed.tooltip')}>{t('workers.col.managed')}</th>
          </tr>
        </thead>
        <tbody>
          {[...filtered].sort((a, b) => {
            const ar = archived.has(a.id) ? 1 : 0
            const br = archived.has(b.id) ? 1 : 0
            return ar - br // archived workers sink to the bottom
          }).map((w) => {
            const suspended = w.managed && w.state === 'suspended'
            const isOffline = w.online === false
            const connection = isOffline ? t('worker.offline') : t('worker.online')
            const isSelected = selectedId === w.id
            const isArchived = archived.has(w.id)
            const typeColor = w.type ? getWorkerTypeColor(w.type, colors) : colors.textDimmed
            return (
              <tr
                key={w.id}
                className={'event-row' + (isSelected ? ' selected' : '')}
                onClick={() => onSelect(w.id)}
                style={{ cursor: 'pointer', userSelect: 'none', opacity: isArchived ? 0.55 : 1 }}
              >
                <td style={cell}>
                  <span
                    onClick={(e) => { e.stopPropagation(); onOpenEvents(w.id) }}
                    title={t('workers.viewEvents.tooltip', { id: w.id })}
                    style={{ color: colors.text, textDecoration: 'underline dotted', cursor: 'pointer' }}
                  >
                    {w.id}
                  </span>
                </td>
                {/* Display name (备注名): what the UI shows in place of the id. */}
                <td style={cell}>
                  {w.label ? (
                    <span title={w.label} style={{ color: colors.textDim }}>{w.label}</span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
                  )}
                </td>
                <td style={cell}>
                  {w.type ? (
                    <span style={{ ...tag, color: typeColor, background: typeColor + '1f', border: '1px solid ' + typeColor + '55' }}>{w.type}</span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
                  )}
                </td>
                {/* Description: a short purpose note, truncated in its column. */}
                <td style={cell}>
                  {w.description ? (
                    <span title={w.description} style={{ color: colors.textDim }}>{w.description}</span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
                  )}
                </td>
                {/* Bus status: online/offline (from the bus channel connection) */}
                <td style={{ ...cell, color: isOffline ? colors.textDimmed : colors.toolCompleted }}>
                  {connection}
                </td>
                {/* Host status: archived dominates; otherwise stopped (declared
                    but not instantiated) / running / suspended */}
                <td style={cell}>
                  {w.managed && isArchived ? (
                    <span style={{ color: colors.textDimmed }}>{t('worker.archived')}</span>
                  ) : w.managed && w.state === 'stopped' ? (
                    <span style={{ color: colors.textDimmed }}>{t('worker.stopped')}</span>
                  ) : w.managed ? (
                    <span style={{ color: suspended ? colors.textDimmed : colors.toolCompleted }}>
                      {suspended ? t('worker.suspended') : t('worker.running')}
                    </span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
                  )}
                </td>
                {/* How the worker is hosted. The two flags are set
                    independently, so managed wins when both are present. A
                    remote worker (connects on its own) is shown as external. */}
                <td style={cell}>
                  {w.managed ? (
                    <span style={tag} title={t('worker.managed.tooltip')}>{t('worker.managed')}</span>
                  ) : w.unmanaged && !w.remote ? (
                    <span style={tag} title={t('worker.subprocess.tooltip')}>{t('worker.subprocess')}</span>
                  ) : (
                    <span style={tag} title={t('worker.external.tooltip')}>{t('worker.external')}</span>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      )}

      <CreateWorkerDialog
        open={showCreate}
        onClose={() => setShowCreate(false)}
        onCreated={(r) => {
          // A remote worker stays in the dialog so its connect configuration
          // can be copied in place; only close for managed/external workers.
          if (!r.remote) setShowCreate(false)
          onRefresh?.()
        }}
        busPort={busPort}
        workers={workers}
      />
      </div>
    </div>
  )
}

// FilterField is a labelled dropdown group (label + the caller's SelectDropdown)
// for the toolbar.
function FilterField({ label, colors, children }: { label: string; colors: Palette; children: React.ReactNode }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 150 }}>
      <span style={{ fontSize: fontSizes.xs, color: colors.textDimmed, whiteSpace: 'nowrap' }}>{label}</span>
      <div style={{ flex: '1 1 auto', minWidth: 110 }}>{children}</div>
    </div>
  )
}
