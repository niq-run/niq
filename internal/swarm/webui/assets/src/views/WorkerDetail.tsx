import { useTheme, fontSizes } from '../theme'
import { type WorkerInfo, type EventPattern } from '../types'
import { getWorkerTypeColor } from '../components/talk-utils'
import { suspendWorker, resumeWorker } from '../services/api'

interface WorkerDetailProps {
  worker: WorkerInfo
  onClose: () => void
}

function fmtPatterns(subs?: EventPattern[]): string {
  if (!subs || subs.length === 0) return '\u2014'
  return subs.map(p => (p.source_id ? `${p.type}@${p.source_id}` : p.type)).join(', ')
}

export default function WorkerDetail({ worker, onClose }: WorkerDetailProps) {
  const { colors } = useTheme()
  const suspended = worker.managed && worker.state === 'suspended'
  const connection = worker.online === false ? 'offline' : 'online'
  const lifecycle = worker.managed ? (suspended ? 'suspended' : 'running') : '\u2014'
  const typeColor = worker.type ? getWorkerTypeColor(worker.type, colors) : colors.textDimmed

  const handleAction = async () => {
    if (suspended) await resumeWorker(worker.id)
    else await suspendWorker(worker.id)
  }

  const tag: React.CSSProperties = {
    display: 'inline-block',
    padding: '0 6px',
    borderRadius: 4,
    fontSize: fontSizes.sm,
    lineHeight: '18px',
    color: typeColor,
    background: typeColor + '1f',
    border: '1px solid ' + typeColor + '55',
  }

  return (
    <div style={{ flex: 1, width: '100%', minWidth: 0, overflowY: 'auto', fontSize: fontSizes.base, background: colors.bg }}>
      {/* Sticky header — full width so content scrolling underneath is covered */}
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          flexWrap: 'wrap',
          position: 'sticky',
          top: 0,
          zIndex: 1,
          background: colors.bg,
          borderBottom: '1px solid ' + colors.border,
          padding: '12px 14px 8px',
        }}
      >
        <span style={{ color: colors.text, fontSize: fontSizes.md }}>{worker.id}</span>
        {worker.type && <span style={tag}>{worker.type}</span>}
        <span
          onClick={onClose}
          className="btn-hover"
          title="close"
          style={{
            cursor: 'pointer',
            marginLeft: 'auto',
            border: '1px solid ' + colors.border,
            borderRadius: 4,
            padding: '0 8px',
            color: colors.textDim,
            fontSize: fontSizes.md,
            lineHeight: '20px',
            userSelect: 'none',
          }}
        >
          {'\u2715'}
        </span>
      </div>

      {/* Scrollable content */}
      <div style={{ padding: 14 }}>
        <div style={{ padding: '10px 12px', background: colors.detailBg, borderRadius: 6, fontSize: fontSizes.base }}>
          <div style={{ display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 16px', alignItems: 'baseline' }}>
            <DetailRow label="ID" value={worker.id} colors={colors} />
            <DetailRow label="Type" value={worker.type || '(none)'} colors={colors} />
            <DetailRow label="Connection" value={connection} colors={colors} />
            <DetailRow label="Lifecycle" value={lifecycle} colors={colors} />
            <DetailRow label="Managed" value={worker.managed ? 'yes' : 'no'} colors={colors} />
            <DetailRow label="Credential" value={worker.credential || '(none)'} colors={colors} />
          </div>

          <div style={{ marginTop: 8, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 8 }}>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 6, textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
              Publish Allow
            </div>
            <div style={{ color: colors.detailValue, wordBreak: 'break-all', lineHeight: 1.5 }}>
              {(worker.publish_allow || []).join(', ') || '\u2014'}
            </div>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, margin: '8px 0 6px', textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
              Subscribe Allow
            </div>
            <div style={{ color: colors.detailValue, wordBreak: 'break-all', lineHeight: 1.5 }}>
              {fmtPatterns(worker.subscribe_allow)}
            </div>
          </div>

          {/* Action area, separated by a horizontal line */}
          <div style={{ marginTop: 12, borderTop: '1px solid ' + colors.detailBorder, paddingTop: 12 }}>
            <div style={{ color: colors.detailLabel, fontSize: fontSizes.base, marginBottom: 10, textTransform: 'uppercase', letterSpacing: '0.5px', fontFamily: 'monospace' }}>
              Actions
            </div>
            {worker.managed ? (
              <span
                onClick={handleAction}
                className="btn-hover"
                style={{ cursor: 'pointer', border: '1px solid ' + colors.border, borderRadius: 4, padding: '2px 10px', color: colors.textDim, fontSize: fontSizes.md, userSelect: 'none' }}
              >
                {suspended ? 'resume' : 'suspend'}
              </span>
            ) : (
              <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm }}>not host-managed</span>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}

function DetailRow({ label, value, colors }: { label: string; value: string; colors: import('../theme').Palette }) {
  return (
    <>
      <span style={{ color: colors.detailLabel, fontSize: fontSizes.base }}>{label}</span>
      <span style={{ color: colors.detailValue, fontSize: fontSizes.base, wordBreak: 'break-all' }}>{value}</span>
    </>
  )
}
