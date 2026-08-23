import { useState } from 'react'
import { useTheme, fontSizes, type Palette } from '../theme'
import { type WorkerInfo, type ViewMode } from '../types'

interface ViewSettings {
  thinkingExpanded: boolean
  compactMode: boolean
  streamingMode: boolean
  responseOnly: boolean
}
type ViewSettingKey = keyof ViewSettings

interface SidebarProps {
  view: ViewMode
  setView: (v: ViewMode) => void
  filterWorkers: Set<string>
  onToggleFilterWorker: (id: string) => void
  workers: WorkerInfo[]
  talkWorkers: Set<string>
  onToggleWorker: (id: string) => void
  viewSettings: ViewSettings
  onToggleViewSetting: (k: ViewSettingKey) => void
  mode: 'control' | 'project'
  project?: string
  projectsOpen: boolean
  onOpenProjects: () => void
}

const VIEW_LABELS: Record<ViewMode, string> = { talk: 'Talk', events: 'Events', workers: 'Workers' }

export default function Sidebar({ view, setView, filterWorkers, onToggleFilterWorker, workers, talkWorkers, onToggleWorker, viewSettings, onToggleViewSetting, mode, project, projectsOpen, onOpenProjects }: SidebarProps) {
  const [rotation, setRotation] = useState(0)
  const { dark, toggle, colors } = useTheme()

  const handleWorkerClick = (id: string) => {
    if (view === 'talk') {
      onToggleWorker(id)
    } else {
      onToggleFilterWorker(id)
    }
  }

  return (
    <div
      style={{
        width: 230,
        minWidth: 230,
        flexShrink: 0,
        borderRight: '1px solid ' + colors.border,
        padding: 16,
        display: 'flex',
        flexDirection: 'column',
        gap: 6,
        overflowY: 'auto',
      }}
    >
      {/* Logo */}
      <div style={{ marginBottom: 4 }}>
        <h2 style={{ margin: 0, color: colors.accent, fontSize: 40, fontFamily: "'Fira Mono', 'PT Mono', monospace", fontWeight: 'normal' }}>
          <span
            onClick={() => setRotation(r => r + 360)}
            style={{ cursor: 'pointer', display: 'inline-block', transform: `perspective(200px) rotateY(${rotation}deg)`, transition: 'transform 1s ease-in-out' }}
          >
            <span style={{ display: 'inline-block', transform: 'scaleX(-1)' }}>n</span>
            <span style={{ display: 'inline-block', transform: 'scaleX(-1)' }}>i</span>
            <span style={{ display: 'inline-block', transform: 'scaleX(-1)' }}>p</span>
          </span>
        </h2>

        {/* Current project name — plain text below the logo. */}
        {mode === 'project' && project && (
          <div
            style={{
              marginTop: 16,
              color: colors.textDim,
              fontSize: fontSizes.sm,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
            title={project}
          >
            {project}
          </div>
        )}
      </div>

      {/* View selector — the three agent views; absent in control mode (no
          project is attached, so talk/events/workers are unavailable) */}
      {mode === 'project' && (<>
      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '8px 0 16px 0' }} />
      <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>View</strong>
      {(Object.keys(VIEW_LABELS) as ViewMode[]).map((v) => (
        <div
          key={v}
          onClick={() => setView(v)}
          style={{ cursor: 'pointer', color: view === v ? colors.accent : colors.textDim, fontSize: fontSizes.md, lineHeight: '20px' }}
        >
          {VIEW_LABELS[v]}{view === v ? ' \u25C9' : ''}
        </div>
      ))}

      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px 0' }} />

      {/* Worker selector list — hidden in the workers view (the main area IS
          the worker list there) */}
      {view !== 'workers' && (
        <>
          <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>Worker Selector</strong>
          {workers.filter(w => view !== 'talk' || w.type === 'reason').map((w) => {
            const isActive = view === 'talk'
              ? talkWorkers.has(w.id)
              : filterWorkers.has(w.id)
            const connection = w.online === false ? 'offline' : 'online'
            if (view === 'talk') {
              // Single line: every entry here is a reason worker, so the type
              // is redundant — just name + connection status.
              return (
                <div
                  key={w.id}
                  onClick={() => handleWorkerClick(w.id)}
                  style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4, minWidth: 0 }}
                >
                  <CheckBox active={isActive} accent={colors.accent} bg={colors.bg} border={colors.border} />
                  <span
                    title={w.id}
                    style={{ color: isActive ? colors.accent : colors.text, fontSize: fontSizes.md, lineHeight: '20px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', flex: 1, minWidth: 0 }}
                  >
                    {w.id}
                  </span>
                  <span style={{ color: connection === 'offline' ? colors.textDimmed : colors.toolCompleted, fontSize: fontSizes.xs, flexShrink: 0 }}>
                    {connection}
                  </span>
                </div>
              )
            }
            // Events view: two lines, type + connection status.
            return (
              <div key={w.id} style={{ marginBottom: 6 }}>
                <div onClick={() => handleWorkerClick(w.id)} style={{ cursor: 'pointer', display: 'flex', alignItems: 'flex-start', gap: 6 }}>
                  <CheckBox active={isActive} accent={colors.accent} bg={colors.bg} border={colors.border} />
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <div
                      title={w.id}
                      style={{ color: isActive ? colors.accent : colors.text, fontSize: fontSizes.md, lineHeight: '20px', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}
                    >
                      {w.id}
                    </div>
                    <div style={{ fontSize: fontSizes.xs, lineHeight: '16px', color: colors.textDimmed, display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                      {w.type ? <span style={{ fontStyle: 'italic' }}>{w.type}</span> : null}
                      <span style={{ color: connection === 'offline' ? colors.textDimmed : colors.toolCompleted }}>{connection}</span>
                    </div>
                  </div>
                </div>
              </div>
            )
          })}

          {/* View Settings — talk view only */}
          {view === 'talk' && (
            <>
              <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px 0' }} />
              <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>View Settings</strong>
              <ToggleRow label="Expand Thinking" on={viewSettings.thinkingExpanded} onToggle={() => onToggleViewSetting('thinkingExpanded')} colors={colors} />
              <ToggleRow label="Compact Mode" on={viewSettings.compactMode} onToggle={() => onToggleViewSetting('compactMode')} colors={colors} />
              <ToggleRow label="Streaming Mode" on={viewSettings.streamingMode} onToggle={() => onToggleViewSetting('streamingMode')} colors={colors} />
              <ToggleRow label="Response Only" on={viewSettings.responseOnly} onToggle={() => onToggleViewSetting('responseOnly')} colors={colors} />
            </>
          )}
        </>
      )}
      </>)}

      {/* Projects — the 4th category. For a project instance it is a jump/start
          hop to other projects; in control mode it is the only usable thing. */}
      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px 0' }} />
      <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>Projects</strong>
      <div
        onClick={onOpenProjects}
        style={{ cursor: 'pointer', color: projectsOpen ? colors.accent : colors.textDim, fontSize: fontSizes.md, lineHeight: '20px' }}
      >
        Projects{projectsOpen ? ' \u25C9' : ''}
      </div>

      {/* Spacer + theme toggle at bottom */}
      <div style={{ flex: 1 }} />
      <div
        onClick={toggle}
        style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm, marginTop: 16 }}
      >
        {dark ? 'light' : 'dark'}
      </div>
    </div>
  )
}

// A small drawn checkbox for the selector state: a bordered box that fills
// with the accent color and shows a checkmark when active.
function CheckBox({ active, accent, bg, border }: { active: boolean; accent: string; bg: string; border: string }) {
  return (
    <span
      style={{
        width: 13,
        height: 13,
        marginTop: 2,
        flexShrink: 0,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        border: '1px solid ' + (active ? accent : border),
        borderRadius: 3,
        background: active ? accent : 'transparent',
      }}
    >
      {active && (
        <span
          style={{
            display: 'inline-block',
            width: 5,
            height: 9,
            border: 'solid ' + bg,
            borderWidth: '0 2px 2px 0',
            transform: 'rotate(45deg)',
            marginTop: -2,
          }}
        />
      )}
    </span>
  )
}

// A labeled switch row (the pill toggle), shared by the view settings.
function ToggleRow({ label, on, onToggle, colors }: { label: string; on: boolean; onToggle: () => void; colors: Palette }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, marginBottom: 8 }}>
      <span style={{ color: colors.textDim, fontSize: fontSizes.sm }}>{label}</span>
      <div
        onClick={onToggle}
        style={{
          width: 32,
          height: 18,
          borderRadius: 9,
          background: on ? colors.accent : colors.textDim,
          cursor: 'pointer',
          position: 'relative',
          transition: 'background 0.15s',
          flexShrink: 0,
        }}
      >
        <div
          style={{
            width: 14,
            height: 14,
            borderRadius: 7,
            background: '#fff',
            position: 'absolute',
            top: 2,
            left: on ? 16 : 2,
            transition: 'left 0.15s',
          }}
        />
      </div>
    </div>
  )
}
