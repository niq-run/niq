import { useState } from 'react'
import { useTheme, fontSizes } from '../theme'
import { useI18n } from '../i18n'
import { usePolling } from '../hooks/usePolling'
import ViewHeader from '../components/ViewHeader'
import type { ProgramInfo } from '../types'

// ProgramsView is the WebUI's program browser: it lists every program under the
// attached project (the project's programs/ directory), showing each program's
// name, form/type, description, tags and sub-content count. It needs a running
// project — without one (control mode) it shows a hint instead.
interface ProgramsViewProps {
  project?: string
  isMobile?: boolean
}

export default function ProgramsView({ project, isMobile }: ProgramsViewProps) {
  const { colors, dark } = useTheme()
  const { t } = useI18n()
  const [programs, setPrograms] = useState<ProgramInfo[]>([])

  // Poll the attached project for its programs. No project → no request.
  // On a project attach/switch, jump to the new one at once instead of waiting
  // for the next poll tick.
  usePolling<ProgramInfo[]>(project ? '/api/programs' : '', 5000, setPrograms, !!project)

  const header = (
    <ViewHeader title={t('programs.title')} count={programs.length} />
  )

  const cellStyle: React.CSSProperties = { padding: '8px 12px', fontSize: fontSizes.sm, verticalAlign: 'top' }
  const labelStyle = (c: any): React.CSSProperties => ({
    display: 'inline-block',
    marginLeft: 6,
    padding: '1px 7px',
    borderRadius: 3,
    border: '1px solid ' + (dark ? colors.textDimmed : colors.border),
    color: colors.textDim,
    fontSize: fontSizes.xs,
    textTransform: 'capitalize',
  })

  return (
    <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
      {!isMobile && header}
      <div style={{ flex: 1, minWidth: 0, overflowY: 'auto', padding: '12px 0 24px' }}>
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
                <tr key={p.name} style={{ borderBottom: '1px solid ' + colors.border }}>
                  <td style={{ ...cellStyle, color: p.locked ? colors.accent : colors.text, fontWeight: p.locked ? 'bold' : 'normal' }}>
                    {p.name}
                    {p.locked && <span style={labelStyle(colors)}>{t('programs.locked')}</span>}
                  </td>
                  <td style={cellStyle}>
                    {p.form_type && <span style={{ color: colors.textDim }}>{p.form_type}</span>}
                  </td>
                  <td style={cellStyle}>
                    {p.content_type && (
                      <span style={{ color: colors.textDim }}>
                        {p.content_type === 'instruction' ? t('programs.contentType.instruction') : p.content_type === 'playbook' ? t('programs.contentType.playbook') : p.content_type}
                      </span>
                    )}
                  </td>
                  <td style={{ ...cellStyle, color: colors.textMuted }}>{p.description}</td>
                  <td style={cellStyle}>
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
                      {(p.tags || []).map((tag) => (
                        <span key={tag} style={labelStyle(colors)}>{tag}</span>
                      ))}
                    </div>
                  </td>
                  <td style={{ ...cellStyle, color: colors.textDim }}>
                    {p.contents !== undefined ? String(p.contents) : ''}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}