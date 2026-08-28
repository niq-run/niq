import { useState, useRef, type CSSProperties, type PointerEvent as ReactPointerEvent, type MouseEvent as ReactMouseEvent } from 'react'
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
  archived: Set<string>
  panel: 'projects' | 'templates' | null
  onSelectPanel: (p: 'projects' | 'templates') => void
  // Mobile drawer mode: the sidebar slides in from the left and overlays the
  // main area; open toggles it, onNavigate is called after any navigation so
  // the caller can close the drawer.
  isMobile: boolean
  open: boolean
  onNavigate: () => void
}

const VIEW_LABELS: Record<ViewMode, string> = { talk: 'Talk', events: 'Events', workers: 'Workers' }

// Sidebar resize: the draggable width range, persisted in localStorage.
const DEFAULT_SIDEBAR_WIDTH = 230
const MIN_SIDEBAR_WIDTH = 180
function loadSidebarWidth(): number {
  const v = parseInt(globalThis.localStorage?.getItem('niq-sidebar-width') ?? '', 10)
  if (Number.isNaN(v)) return DEFAULT_SIDEBAR_WIDTH
  return Math.min(Math.max(v, MIN_SIDEBAR_WIDTH), Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.5))
}

export default function Sidebar({ view, setView, filterWorkers, onToggleFilterWorker, workers, talkWorkers, onToggleWorker, viewSettings, onToggleViewSetting, mode, project, panel, onSelectPanel, archived, isMobile, open, onNavigate }: SidebarProps) {
  const { dark, toggle, colors } = useTheme()

  // ── Logo drag: the logo can be dragged sideways and springs back. Dragging
  // it all the way right toggles a mirror flip that plays out while the logo
  // slides back to its resting position.
  const [dragX, setDragX] = useState(0)
  const [dragging, setDragging] = useState(false)
  const [flipped, setFlipped] = useState(false)
  const [hittingEdge, setHittingEdge] = useState(false)

  // Desktop sidebar width is user-draggable (like the detail panels) and
  // persists across reloads; double-clicking the handle resets it. Mobile
  // keeps the 80% drawer.
  const [sidebarWidth, setSidebarWidth] = useState(loadSidebarWidth)
  const [resizeHover, setResizeHover] = useState(false)
  const sidebarWidthRef = useRef(sidebarWidth)
  const handleResizeStart = (e: ReactMouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = sidebarWidthRef.current
    const onMove = (ev: MouseEvent) => {
      const next = startW + (ev.clientX - startX)
      const clamped = Math.min(Math.max(next, MIN_SIDEBAR_WIDTH), Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.5))
      sidebarWidthRef.current = clamped
      setSidebarWidth(clamped)
      globalThis.localStorage?.setItem('niq-sidebar-width', String(clamped))
    }
    const onUp = () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      document.body.style.cursor = ''
    }
    document.body.style.cursor = 'col-resize'
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }
  const handleResizeReset = () => {
    sidebarWidthRef.current = DEFAULT_SIDEBAR_WIDTH
    setSidebarWidth(DEFAULT_SIDEBAR_WIDTH)
    globalThis.localStorage?.setItem('niq-sidebar-width', String(DEFAULT_SIDEBAR_WIDTH))
  }
  // Logo motion. Two separate timings:
  //  - durFor: the single-click round trip (out to the apex and back), which
  //    plays as one continuous move and keeps its slower, distance-scaled
  //    duration so the perceived speed stays even across platforms.
  //  - returnDurFor: the spring-back released at the end of a manual drag. That
  //    one is deliberately quick — the finger already did the work, so the logo
  //    should come home promptly instead of lingering at the rightmost apex.
  const [moveDur, setMoveDur] = useState('0.5s')
  const lastDragX = useRef(0)
  const durFor = (px: number) => Math.max(140, Math.round((px / 300) * 1000)) + 'ms'
  // Spring-back: roughly half of durFor, still scaled by distance.
  const returnDurFor = (px: number) => Math.max(110, Math.round((px / 300) * 1000)) + 'ms'
  // Decelerating bezier (fast off the apex, slow into rest) so the return reads
  // as fast-then-slow rather than the old slow-fast-slow ease-in-out.
  const logoEasing = 'cubic-bezier(0.2, 1, 0.7, 1)'
  const draggingRef = useRef(false)
  const dragStartX = useRef(0)
  const dragMoved = useRef(false)
  const armedRef = useRef(false)
  const logoWrapRef = useRef<HTMLHeadingElement>(null)
  const logoRef = useRef<HTMLSpanElement>(null)

  const onLogoPointerDown = (e: ReactPointerEvent) => {
    // A click cycle may be holding its end frame via the Web Animations API;
    // cancel it so the CSS transform fully owns the drag.
    logoRef.current?.getAnimations().forEach(a => a.cancel())
    draggingRef.current = true
    dragMoved.current = false
    dragStartX.current = e.clientX
    setDragging(true)
    setHittingEdge(false)
    setDragX(0)
    e.currentTarget.setPointerCapture?.(e.pointerId)
  }
  const onLogoPointerMove = (e: ReactPointerEvent) => {
    if (!draggingRef.current) return
    const dx = e.clientX - dragStartX.current
    // Drag range = the padded content area, plus a small allowance past the
    // right divider (so the logo visibly "hits" it); the divider glows while
    // the logo is against it.
    const padX = isMobile ? 24 : 16
    const wrap = logoWrapRef.current
    const span = logoRef.current
    const maxRight = wrap && span ? Math.max(0, wrap.clientWidth - span.offsetWidth + padX + 8) : padX + 40
    const clamped = Math.max(-padX, Math.min(maxRight, dx))
    const atEdge = clamped >= maxRight - 20
    setHittingEdge(atEdge)
    if (atEdge) armedRef.current = true
    if (Math.abs(clamped) > 5) dragMoved.current = true
    lastDragX.current = clamped
    setDragX(clamped)
  }
  const onLogoPointerUp = (e: ReactPointerEvent) => {
    if (!draggingRef.current) return
    draggingRef.current = false
    setDragging(false)
    setHittingEdge(false)
    if (armedRef.current) {
      armedRef.current = false
      setFlipped(f => !f) // mirror flip animates together with the spring-back
    }
    setMoveDur(returnDurFor(Math.abs(lastDragX.current))) // quick, decelerating return
    setDragX(0) // spring back to the resting position
    e.currentTarget.releasePointerCapture?.(e.pointerId)
  }

  // Single click plays the complete back-and-forth in ONE continuous motion:
  // slow start, fastest at the rightmost apex, slow return — the Web Animations
  // API applies ease-in on the way out (peaking at the apex) and ease-out on
  // the way back, with a mirror flip through the apex. Manual drag stays on the
  // CSS transition.
  const playFullCycle = () => {
    const el = logoRef.current
    if (!el) return
    const dist = el.offsetWidth > 0 ? el.offsetWidth : 60 // one logo width
    const dur = parseInt(durFor(dist * 2)) // round trip at the baseline speed
    const cur = flipped ? 180 : 0
    const persp = 'perspective(200px) '
    // Commit the end state first so the CSS transform matches the animation's
    // final frame (fill: forwards holds it; a drag cancels it).
    setFlipped(f => !f)
    el.animate([
      // Two ease profiles, keep one active (the other is commented out):
      //  A — fast-slow-fast: strong ease-out out (fast start → slow into the
      //      apex) + strong ease-in back (slow off the apex → fast home).
      // { transform: persp + `translateX(0) rotateY(${cur}deg)`, easing: 'cubic-bezier(0, 0, 0.3, 1)' },
      // { transform: persp + `translateX(${dist}px) rotateY(${cur + 90}deg)`, offset: 0.5, easing: 'cubic-bezier(0.7, 0, 1, 1)' },
      //  B — slow-fast-slow: strong ease-in out (slow start → sharp at the
      //      apex) + strong ease-out back (fast off the apex → slow to rest).
      { transform: persp + `translateX(0) rotateY(${cur}deg)`, easing: 'cubic-bezier(0.7, 0, 1, 1)' },
      { transform: persp + `translateX(${dist}px) rotateY(${cur + 90}deg)`, offset: 0.5, easing: 'cubic-bezier(0, 0, 0.3, 1)' },
      { transform: persp + `translateX(0) rotateY(${cur + 180}deg)` },
    ], { duration: dur, fill: 'forwards' })
  }

  const onLogoClick = () => {
    if (dragMoved.current) {
      dragMoved.current = false // consume the click that ends a drag
      return
    }
    playFullCycle()
  }

  const handleWorkerClick = (id: string) => {
    if (view === 'talk') {
      onToggleWorker(id)
    } else {
      onToggleFilterWorker(id)
    }
    onNavigate()
  }

  // Desktop: a fixed left column. Mobile: an off-canvas drawer (fixed,
  // translated off-screen when closed) that overlays the main area.
  // The right divider turns accent-colored and glows while the logo is dragged
  // against it, then fades when the logo leaves.
  const accent = colors.accent
  const accentDim = colors.accentDim
  const dividerColor = hittingEdge ? accent : colors.border
  const dividerGlow = hittingEdge ? 'inset -2px 0 8px ' + accentDim : 'none'
  const glowTransition = 'border-color 0.2s ease, box-shadow 0.2s ease'

  const rootStyle: React.CSSProperties = isMobile ? {
    position: 'fixed',
    top: 0,
    left: 0,
    bottom: 0,
    width: '80%',
    minWidth: 240,
    maxWidth: 340,
    zIndex: 30,
    transform: open ? 'translateX(0)' : 'translateX(-100%)',
    transition: 'transform 0.2s ease, ' + glowTransition,
    boxShadow: open
      ? '2px 0 10px rgba(0,0,0,0.2)' + (hittingEdge ? ', ' + dividerGlow : '')
      : dividerGlow,
    background: colors.bg,
    borderRight: '1px solid ' + dividerColor,
    padding: '16px 24px',
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
  } : {
    width: sidebarWidth,
    minWidth: MIN_SIDEBAR_WIDTH,
    flexShrink: 0,
    position: 'relative',
    borderRight: '1px solid ' + dividerColor,
    boxShadow: dividerGlow,
    transition: glowTransition,
    padding: 16,
    display: 'flex',
    flexDirection: 'column',
    gap: 6,
  }

  // Mobile touch: bigger text and taller hit areas so the options are easy to
  // tap on a phone. Desktop keeps the compact sizes.
  const optSize = isMobile ? fontSizes.xl : fontSizes.md
  const optLineNum = isMobile ? 40 : 20
  const optLine = optLineNum + 'px'
  const optPad = isMobile ? '4px 0' : undefined
  // Horizontal content inset; mobile gets more breathing room on both sides.
  // The section dividers use a matching negative horizontal margin so they
  // stay edge-to-edge.
  const contentPadX = isMobile ? 24 : 16
  const hrX = isMobile ? -24 : -16
  // Checkbox size: a bit bigger on mobile for easier tapping.
  const checkSize = isMobile ? 16 : 13

  return (
    <>
      {isMobile && open && (
        <div
          onClick={onNavigate}
          style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.4)', zIndex: 29 }}
        />
      )}
      <div style={rootStyle}>
      {/* Desktop only: drag handle on the right edge to resize the sidebar.
          Same geometry as the detail-panel handle: a 12px strip, the divider
          line flush with the right edge, and the grip centered on it. */}
      {!isMobile && (
        <div
          onMouseDown={handleResizeStart}
          onDoubleClick={handleResizeReset}
          onMouseEnter={() => setResizeHover(true)}
          onMouseLeave={() => setResizeHover(false)}
          title="drag to resize, double-click to reset"
          style={{ position: 'absolute', top: 0, bottom: 0, right: 0, width: 12, cursor: 'col-resize', zIndex: 5, userSelect: 'none' }}
        >
          {/* Thin full-height line, flush with the sidebar's right edge (the divider) */}
          <div style={{ position: 'absolute', top: 0, bottom: 0, right: 0, width: 1, background: colors.border }} />
          {/* Grip centered on the line, painted above it */}
          <div style={{ position: 'absolute', top: '50%', right: -2, width: 5, height: 30, marginTop: -15, borderRadius: 3, background: resizeHover ? colors.textDim : colors.textDimmed, zIndex: 1, transition: 'background 0.15s' }} />
        </div>
      )}
      {/* Fixed header (close + logo + project) — never scrolls */}
      <div style={{ flexShrink: 0 }}>
      {/* Mobile only: collapse button for the drawer */}
      {isMobile && (
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 4 }}>
          <button
            onClick={onNavigate}
            title="close"
            style={{ background: 'none', border: '1px solid ' + colors.border, borderRadius: 4, color: colors.textDim, fontSize: fontSizes.md, lineHeight: 1, padding: '4px 8px', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}
          >
            ✕
          </button>
        </div>
      )}
      {/* Logo + project name — clicking anywhere here plays the single-click
          cycle (a drag in the logo suppresses it). */}
      <div style={{ marginBottom: 4 }} onClick={onLogoClick}>
        <h2 ref={logoWrapRef} style={{ margin: '0 0 0 -3px', lineHeight: 1, color: colors.accent, fontSize: 40, fontFamily: "'SomeType Mono', 'Fira Mono', 'PT Mono', monospace", fontWeight: 'bold' }}>
          <span
            ref={logoRef}
            onPointerDown={onLogoPointerDown}
            onPointerMove={onLogoPointerMove}
            onPointerUp={onLogoPointerUp}
            onPointerCancel={onLogoPointerUp}
            style={{
              cursor: dragging ? 'grabbing' : 'grab',
              display: 'inline-block',
              transform: `perspective(200px) translateX(${dragX}px) rotateY(${flipped ? 180 : 0}deg)`,
              // No transition while dragging; on release the transform springs
              // back with a decelerating bezier (fast-then-slow) and a short,
              // distance-scaled duration.
              transition: dragging ? 'none' : `transform ${moveDur} ${logoEasing}`,
              // Swallow touch gestures on the logo so the drag wins over page
              // scrolling on any touch device.
              touchAction: 'none',
              // Dragging must not select the logo text.
              userSelect: 'none',
              WebkitUserSelect: 'none',
            }}
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
              marginTop: 40,
              color: colors.textDim,
              fontSize: fontSizes.sm,
              whiteSpace: 'nowrap',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
            title={`[${project}]`}
          >
            {`[${project}]`}
          </div>
        )}
      </div>
      </div>

      {/* Scrollable options between the fixed header and footer. The negative
          horizontal margin lets the section dividers span edge-to-edge (into
          the sidebar's padding), while the padding keeps the text inset. The
          flex column + gap reproduces the sidebar's original item spacing. */}
      <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', overflowX: 'hidden', margin: isMobile ? '0 -24px' : '0 -16px', padding: `0 ${contentPadX}px`, display: 'flex', flexDirection: 'column', gap: 6 }}>

      {/* View selector — the three agent views; absent in control mode (no
          project is attached, so talk/events/workers are unavailable) */}
      {mode === 'project' && (<>
      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '8px ' + hrX + 'px 16px' }} />
      <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>View</strong>
      {(Object.keys(VIEW_LABELS) as ViewMode[]).map((v) => (
        <div
          key={v}
          onClick={() => { setView(v); onNavigate() }}
          style={{ cursor: 'pointer', color: view === v ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine, padding: optPad }}
        >
          {VIEW_LABELS[v]}{view === v ? ' \u25C9' : ''}
        </div>
      ))}

      {/* Worker selector list — hidden in the workers view (the main area IS
          the worker list there). Its separator is hidden too, so switching to
          the workers view doesn't leave a stray line. */}
      {view !== 'workers' && (
        <>
          <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px ' + hrX + 'px' }} />
          <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>Worker Selector</strong>
          {workers.filter(w => (view !== 'talk' || w.type === 'reason') && !archived.has(w.id)).map((w) => {
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
                  style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4, minWidth: 0, padding: optPad }}
                >
                  <CheckBox active={isActive} accent={colors.accent} bg={colors.bg} border={colors.border} size={checkSize} />
                  <span
                    title={w.id}
                    style={{ color: isActive ? colors.accent : colors.text, fontSize: optSize, lineHeight: optLine, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', flex: 1, minWidth: 0 }}
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
                <div onClick={() => handleWorkerClick(w.id)} style={{ cursor: 'pointer', display: 'flex', alignItems: 'flex-start', gap: 6, padding: optPad }}>
                  {/* marginTop centers the box against the first text line,
                      whose glyph sits mid-way in its (tall) line box. */}
                  <CheckBox active={isActive} accent={colors.accent} bg={colors.bg} border={colors.border} size={checkSize} style={{ marginTop: (optLineNum - checkSize) / 2 }} />
                  <div style={{ minWidth: 0, flex: 1 }}>
                    <div
                      title={w.id}
                      style={{ color: isActive ? colors.accent : colors.text, fontSize: optSize, lineHeight: optLine, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}
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
              <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px ' + hrX + 'px' }} />
              <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>View Settings</strong>
              <ToggleRow label="Expand Thinking" on={viewSettings.thinkingExpanded} onToggle={() => onToggleViewSetting('thinkingExpanded')} colors={colors} mobile={isMobile} />
              <ToggleRow label="Compact Mode" on={viewSettings.compactMode} onToggle={() => onToggleViewSetting('compactMode')} colors={colors} mobile={isMobile} />
              <ToggleRow label="Streaming Mode" on={viewSettings.streamingMode} onToggle={() => onToggleViewSetting('streamingMode')} colors={colors} mobile={isMobile} />
              <ToggleRow label="Response Only" on={viewSettings.responseOnly} onToggle={() => onToggleViewSetting('responseOnly')} colors={colors} mobile={isMobile} />
            </>
          )}
        </>
      )}
      </>)}

      {/* Projects — the 4th category: project + template management. For a
          project instance it is a jump/start hop to other projects; in control
          mode it is the only usable thing. */}
      <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px ' + hrX + 'px' }} />
      <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>Projects</strong>
      <div
        onClick={() => { onSelectPanel('projects'); onNavigate() }}
        style={{ cursor: 'pointer', color: panel === 'projects' ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine, padding: optPad }}
      >
        Projects{panel === 'projects' ? ' \u25C9' : ''}
      </div>
      <div
        onClick={() => { onSelectPanel('templates'); onNavigate() }}
        style={{ cursor: 'pointer', color: panel === 'templates' ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine, padding: optPad }}
      >
        Templates{panel === 'templates' ? ' \u25C9' : ''}
      </div>

      </div>

      {/* Theme toggle — fixed footer, stays put when the options scroll */}
      <div style={{ flexShrink: 0, marginTop: 16 }}>
        <div
          onClick={toggle}
          style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm }}
        >
          {dark ? 'light' : 'dark'}
        </div>
      </div>
      </div>
    </>
  )
}

// A small drawn checkbox for the selector state: a bordered box that fills
// with the accent color and shows a checkmark when active.
function CheckBox({ active, accent, bg, border, size = 13, style }: { active: boolean; accent: string; bg: string; border: string; size?: number; style?: CSSProperties }) {
  return (
    <span
      style={{
        width: size,
        height: size,
        flexShrink: 0,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        border: '1px solid ' + (active ? accent : border),
        borderRadius: 3,
        background: active ? accent : 'transparent',
        ...style,
      }}
    >
      {active && (
        <span
          style={{
            display: 'inline-block',
            width: size * 0.38,
            height: size * 0.72,
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
function ToggleRow({ label, on, onToggle, colors, mobile = false }: { label: string; on: boolean; onToggle: () => void; colors: Palette; mobile?: boolean }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, marginBottom: mobile ? 14 : 8, padding: mobile ? '4px 0' : undefined }}>
      <span style={{ color: colors.textDim, fontSize: mobile ? fontSizes.md : fontSizes.sm }}>{label}</span>
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
