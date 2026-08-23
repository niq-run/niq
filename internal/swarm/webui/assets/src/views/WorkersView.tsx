import { useTheme, fontSizes } from '../theme'
import { type WorkerInfo } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'

interface WorkersViewProps {
  workers: WorkerInfo[]
  selectedId: string | null
  onSelect: (id: string) => void
  onOpenEvents: (id: string) => void
  archived: Set<string>
}

export default function WorkersView({ workers, selectedId, onSelect, onOpenEvents, archived }: WorkersViewProps) {
  const { colors } = useTheme()

  // Single-line cells with ellipsis (same as the events table), with a taller
  // row: the worker list is less dense than the event stream.
  const cell: React.CSSProperties = {
    padding: '10px 6px',
    verticalAlign: 'top',
    borderBottom: '1px solid ' + colors.eventRowBorder,
    whiteSpace: 'nowrap',
    overflow: 'hidden',
    textOverflow: 'ellipsis',
    maxWidth: 0,
  }

  // Tag badge, matching the events table's type tag but in a neutral tone.
  const tag: React.CSSProperties = {
    display: 'inline-block',
    padding: '0 6px',
    borderRadius: 4,
    fontSize: fontSizes.sm,
    lineHeight: '18px',
    color: colors.textDimmed,
    background: colors.textDimmed + '1f',
    border: '1px solid ' + colors.textDimmed + '55',
    whiteSpace: 'nowrap',
  }

  return (
    <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: '24px 24px 16px 24px' }}>
      <div style={{ marginBottom: 12, fontSize: fontSizes.xl, color: colors.text }}>
        Workers{' '}
        <span style={{ color: colors.textMuted, fontSize: fontSizes.md }}>({workers.length})</span>
      </div>

      <table style={{ width: '100%', borderCollapse: 'collapse', tableLayout: 'fixed', fontSize: fontSizes.md }}>
        <thead>
          <tr style={{ textAlign: 'left', color: colors.textDimmed, fontSize: fontSizes.xs }}>
            <th style={{ padding: '6px 6px', width: 190, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }}>Worker ID</th>
            <th style={{ padding: '6px 6px', width: 110, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }}>Type</th>
            <th style={{ padding: '6px 6px', width: 90, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="bus connection status (online/offline)">Connection</th>
            <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="host-managed lifecycle state (running/suspended)">Lifecycle</th>
          </tr>
        </thead>
        <tbody>
          {workers.map((w) => {
            const suspended = w.managed && w.state === 'suspended'
            const connection = w.online === false ? 'offline' : 'online'
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
                    title={'view events from ' + w.id}
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
                <td style={{ ...cell, color: connection === 'offline' ? colors.textDimmed : colors.toolCompleted }}>
                  {connection}
                </td>
                {/* Host status: running/suspended for managed workers, dash for unmanaged */}
                <td style={cell}>
                  {w.managed ? (
                    <span style={{ color: suspended ? colors.textDimmed : colors.toolCompleted }}>
                      {suspended ? 'suspended' : 'running'}
                    </span>
                  ) : (
                    <span style={{ color: colors.textDimmed }}>{'\u2014'}</span>
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
