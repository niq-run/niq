import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'

interface WorkersViewProps {
  workers: WorkerInfo[]
  selectedId: string | null
  onSelect: (id: string) => void
  onOpenEvents: (id: string) => void
  archived: Set<string>
  isMobile: boolean
  onRefresh?: () => void
}

export default function WorkersView({ workers, selectedId, onSelect, onOpenEvents, archived, isMobile, onRefresh }: WorkersViewProps) {
  const { colors } = useTheme()
  const { t } = useI18n()

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
    <div style={{ flex: 1, minWidth: 0, overflow: 'auto', padding: '24px 24px 16px 24px' }}>
      {/* On mobile the app-level top bar heads the page, so the in-view header
          is skipped (same as the talk/events views). */}
      {!isMobile && (
        <div style={{ marginBottom: 12, fontSize: fontSizes.xl, color: colors.text, display: 'flex', alignItems: 'baseline', gap: 10 }}>
          {t('workers.title')}{' '}
          <span style={{ color: colors.textMuted, fontSize: fontSizes.md }}>({workers.length})</span>
          {onRefresh && (
            <span
              onClick={onRefresh}
              className="btn-hover"
              title={t('workers.refresh.tooltip')}
              style={{ marginLeft: 'auto', cursor: 'pointer', fontSize: fontSizes.sm, color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 4, padding: '3px 10px', userSelect: 'none' }}
            >
              {t('workers.refresh')}
            </span>
          )}
        </div>
      )}

      {/* min-width lets the fixed columns scroll horizontally on narrow
          (phone) viewports instead of collapsing. */}
      <table style={{ width: '100%', minWidth: 640, borderCollapse: 'collapse', tableLayout: 'fixed', fontSize: fontSizes.md }}>
        <thead>
          <tr style={{ textAlign: 'left', color: colors.textDimmed, fontSize: fontSizes.xs }}>
            <th style={{ padding: '6px 6px', width: 170, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }}>{t('workers.col.workerId')}</th>
            <th style={{ padding: '6px 6px', width: 110, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }}>{t('workers.col.type')}</th>
            <th style={{ padding: '6px 6px', width: 90, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.connection.tooltip')}>{t('workers.col.connection')}</th>
            <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.lifecycle.tooltip')}>{t('workers.col.lifecycle')}</th>
            <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('workers.col.managed.tooltip')}>{t('workers.col.managed')}</th>
          </tr>
        </thead>
        <tbody>
          {[...workers].sort((a, b) => {
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
                <td style={cell}>
                  {w.type ? (
                    <span style={{ ...tag, color: typeColor, background: typeColor + '1f', border: '1px solid ' + typeColor + '55' }}>{w.type}</span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
                  )}
                </td>
                {/* Bus status: online/offline (from the bus channel connection) */}
                <td style={{ ...cell, color: isOffline ? colors.textDimmed : colors.toolCompleted }}>
                  {connection}
                </td>
                {/* Host status: archived dominates; otherwise running/suspended */}
                <td style={cell}>
                  {w.managed && isArchived ? (
                    <span style={{ color: colors.textDimmed }}>{t('worker.archived')}</span>
                  ) : w.managed ? (
                    <span style={{ color: suspended ? colors.textDimmed : colors.toolCompleted }}>
                      {suspended ? t('worker.suspended') : t('worker.running')}
                    </span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
                  )}
                </td>
                {/* How the worker is hosted. The two flags are set
                    independently, so managed wins when both are present. */}
                <td style={cell}>
                  {w.managed ? (
                    <span style={tag} title={t('worker.managed.tooltip')}>{t('worker.managed')}</span>
                  ) : w.unmanaged ? (
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
    </div>
  )
}
