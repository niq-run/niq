import { useState, type CSSProperties } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import ViewHeader from '../components/ViewHeader'
import { formatTime } from '../components/talk-utils'
import CollapsibleCode from '../components/CollapsibleCode'
import type { ApprovalEntry } from '../types'

interface ApprovalsViewProps {
  approvals: ApprovalEntry[]
  onDecide: (id: string, approved: boolean, note?: string) => void
  isMobile: boolean
}

// ApprovalsView lists the approval requests the HIW tracks: pending entries on
// top with approve/reject actions, the decided history below. The parent owns
// the polling; this view renders and decides.
export default function ApprovalsView({ approvals, onDecide, isMobile }: ApprovalsViewProps) {
  const { colors } = useTheme()
  const { t } = useI18n()
  const [noteBy, setNoteBy] = useState<Record<string, string>>({})
  const [busyId, setBusyId] = useState<string | null>(null)

  const pending = approvals.filter(a => !a.decision)
  const decided = approvals.filter(a => a.decision)

  const decide = async (id: string, approved: boolean) => {
    setBusyId(id)
    await onDecide(id, approved, (noteBy[id] ?? '').trim())
    setBusyId(null)
    setNoteBy(prev => ({ ...prev, [id]: '' }))
  }

  const input: CSSProperties = {
    flex: 1,
    minWidth: 140,
    height: 30,
    boxSizing: 'border-box',
    padding: '0 10px',
    fontSize: fontSizes.sm,
    background: colors.bg,
    border: '1px solid ' + colors.border,
    color: colors.text,
    outline: 'none',
  }
  const btn = (accent: boolean): CSSProperties => ({
    cursor: 'pointer',
    height: 30,
    boxSizing: 'border-box',
    background: 'transparent',
    color: accent ? colors.accent : colors.textDim,
    border: '1px solid ' + colors.border,
    borderRadius: 2,
    padding: '0 14px',
    fontSize: fontSizes.sm,
    whiteSpace: 'nowrap',
  })

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {!isMobile && (
        <ViewHeader
          title={t('approvals.title')}
          right={pending.length > 0 ? (
            <span style={{ fontSize: fontSizes.sm, color: colors.textDim }}>
              {t('approvals.pendingCount', { n: pending.length })}
            </span>
          ) : undefined}
        />
      )}
      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: isMobile ? 16 : 24 }}>
      {approvals.length === 0 && (
        <div style={{ color: colors.textDim, fontSize: fontSizes.md }}>{t('approvals.empty')}</div>
      )}

      {pending.map(a => (
        <div
          key={a.id}
          style={{
            border: '1px solid ' + colors.accent,
            borderRadius: 6,
            padding: '12px 16px',
            marginBottom: 10,
            background: colors.bgLight,
          }}
        >
          <ApprovalHead a={a} colors={colors} t={t} />
          <div style={{ display: 'flex', gap: 8, marginTop: 10, flexWrap: 'wrap', alignItems: 'center' }}>
            <input
              style={input}
              value={noteBy[a.id] ?? ''}
              onChange={e => setNoteBy(prev => ({ ...prev, [a.id]: e.target.value }))}
              placeholder={t('approvals.notePlaceholder')}
            />
            <button onClick={() => decide(a.id, true)} disabled={busyId === a.id} style={{ ...btn(true), opacity: busyId === a.id ? 0.6 : 1 }}>
              {t('approvals.approve')}
            </button>
            <button onClick={() => decide(a.id, false)} disabled={busyId === a.id} style={{ ...btn(false), color: colors.toolFailed, opacity: busyId === a.id ? 0.6 : 1 }}>
              {t('approvals.reject')}
            </button>
          </div>
        </div>
      ))}

      {decided.map(a => (
        <div key={a.id} style={{ border: '1px solid ' + colors.border, borderRadius: 6, padding: '12px 16px', marginBottom: 10, opacity: 0.65 }}>
          <ApprovalHead a={a} colors={colors} t={t} />
          <div style={{ marginTop: 8, fontSize: fontSizes.sm, color: a.decision!.approved ? colors.toolCompleted : colors.toolFailed }}>
            {a.decision!.approved ? t('approvals.approved') : t('approvals.rejected')}
            {a.decision!.note ? ` · ${a.decision!.note}` : ''}
            {' · '}{formatTime(a.decision!.timestamp)}
          </div>
        </div>
      ))}
      </div>
    </div>
  )
}

// ApprovalHead is the shared row header: requester, action, the reason for
// the approval, and a generic rendering of the request's payload — every
// field is shown as-is so any future approval kind displays fully.
function ApprovalHead({ a, colors, t }: { a: ApprovalEntry; colors: ReturnType<typeof useTheme>['colors']; t: ReturnType<typeof useI18n>['t'] }) {
  return (
    <div>
      <div style={{ color: colors.text, fontSize: fontSizes.md, wordBreak: 'break-all' }}>
        <strong>{a.worker_id}</strong>
        {a.action ? ` · ${a.action}` : ''}
      </div>
      <div style={{ marginTop: 6, fontSize: fontSizes.sm, color: colors.text }}>
        {t(a.action === 'mount.add' ? 'approval.reason.mount.add' : 'approval.reason.generic')}
      </div>
      {Object.keys(a.payload ?? {}).length > 0 && (
      <div style={{ background: colors.bg, border: '1px solid ' + colors.borderLight, borderRadius: 2, padding: '6px 8px', marginTop: 8 }}>
        {Object.keys(a.payload ?? {}).length > 0 ? (
          <CollapsibleCode code={JSON.stringify(a.payload, null, 2)} language="json" />
        ) : (
          <div style={{ fontSize: fontSizes.sm, color: colors.textDimmed }}>{t('approvals.noPayload')}</div>
        )}
      </div>
      )}
      <div style={{ fontSize: fontSizes.xs, color: colors.textDimmed, marginTop: 4 }}>
        {formatTime(a.timestamp)}
      </div>
    </div>
  )
}
