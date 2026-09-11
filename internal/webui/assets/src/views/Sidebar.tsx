import { useState, useRef, useEffect, useMemo, type CSSProperties, type PointerEvent as ReactPointerEvent, type MouseEvent as ReactMouseEvent } from 'react'
import { useTheme, fontSizes, VIEW_HEADER_HEIGHT, type Palette } from '../theme'
import { useI18n } from '../i18n'
import { type WorkerInfo, type ViewMode, type ViewSettings, type ViewSettingKey } from '../types'
import WorkerPickerModal from '../components/WorkerPickerModal'
import TagFilterDropdown from '../components/TagFilterDropdown'

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
  // Attached project lifecycle for the footer menu: running state from the
  // control-plane poll, in-flight stop/restart op, the banner's start op, and
  // the three actions (same semantics as the projects page rows).
  projRunning?: boolean
  projBusy?: '' | 'stop' | 'restart'
  projStarting?: boolean
  projActionErr?: string
  onProjectStart?: () => void
  onProjectStop?: () => void
  onProjectRestart?: () => void
  archived: Set<string>
  panel: 'projects' | 'templates' | 'providers' | null
  onSelectPanel: (p: 'projects' | 'templates' | 'providers') => void
  // Mobile drawer mode: the sidebar slides in from the left and overlays the
  // main area; open toggles it, onNavigate is called after any navigation so
  // the caller can close the drawer.
  isMobile: boolean
  open: boolean
  onNavigate: () => void
  // Pending approval requests (HIW-tracked) — shown as a nav badge.
  pendingApprovals?: number
}

// Sidebar resize: the draggable width range, persisted in localStorage.
const DEFAULT_SIDEBAR_WIDTH = 230
const MIN_SIDEBAR_WIDTH = 180
function loadSidebarWidth(): number {
  const v = parseInt(globalThis.localStorage?.getItem('niq-sidebar-width') ?? '', 10)
  if (Number.isNaN(v)) return DEFAULT_SIDEBAR_WIDTH
  return Math.min(Math.max(v, MIN_SIDEBAR_WIDTH), Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.5))
}

export default function Sidebar({ view, setView, filterWorkers, onToggleFilterWorker, workers, talkWorkers, onToggleWorker, viewSettings, onToggleViewSetting, mode, project, projRunning, projBusy, projStarting, projActionErr, onProjectStart, onProjectStop, onProjectRestart, panel, onSelectPanel, archived, isMobile, open, onNavigate, pendingApprovals = 0 }: SidebarProps) {
  const { dark, toggle, colors } = useTheme()
  const { lang, setLang, t } = useI18n()
  // Hovered sidebar option (non-toggle, non-checkbox rows) — shows a full-width
  // row highlight. Excludes the view-setting switches and the worker-selector
  // checkbox rows.
  const [hoverId, setHoverId] = useState<string | null>(null)
  // Expanded picker modal for the worker selector (open on the expand button).
  const [showWorkerPicker, setShowWorkerPicker] = useState(false)
  // Multi-select tag filter applied to the inline selector list (and inherited
  // by the expanded picker).
  const [filterTags, setFilterTags] = useState<string[]>([])
  const hoverStyle = (id: string): CSSProperties => ({
    background: hoverId === id ? colors.bgLight : 'transparent',
    // Full-width row highlight that bleeds to the sidebar edges. The options
    // list sits at gap:0 (flush rows), so neighbouring hovered rows form one
    // continuous strip with no gap between their backgrounds. The vertical
    // padding gives the text breathing room and makes the highlight taller
    // (larger inner padding) without any overlap.
    margin: '0 -' + contentPadX + 'px',
    padding: '6px ' + contentPadX + 'px',
    borderRadius: 0,
    transition: 'background 0.12s',
  })

  // View labels must be computed inside the component (not module-level) so
  // they re-render when the language changes.
  const VIEW_LABELS: Record<ViewMode, string> = {
    talk: t('nav.talk'),
    approvals: t('nav.approvals'),
    events: t('nav.events'),
    workers: t('nav.workers'),
    programs: t('sidebar.programs'),
  }

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
    // Only the primary button drags the logo; the right button is reserved for
    // the invert-colors context action on the band.
    if (e.button !== 0) return
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
    const padX = contentPadX
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

  // The worker-selector rows shown inline and in the expanded picker: reason
  // workers when choosing whom to talk to, all workers when filtering events,
  // archived ones always hidden.
  const selectorWorkers = workers.filter(w => (view !== 'talk' || w.type === 'reason') && !archived.has(w.id))
  const selectorSelected = view === 'talk' ? talkWorkers : filterWorkers

  // Tag multi-select filter for the selector list. A worker is kept when it
  // carries any of the selected tags (OR).
  const selectorTags = useMemo(() => {
    const set = new Set<string>()
    for (const w of selectorWorkers) for (const tag of w.tags || []) set.add(tag)
    return [...set].sort()
  }, [selectorWorkers])
  const shownSelectorWorkers = filterTags.length === 0
    ? selectorWorkers
    : selectorWorkers.filter(w => (w.tags || []).some(tag => filterTags.includes(tag)))

  // Footer project menu: opened by clicking the project name in the bottom
  // row; closes on any click outside the menu and its trigger.
  const [projMenuOpen, setProjMenuOpen] = useState(false)
  // Hover state of the project block, for the hover-only border (a transparent
  // border is laid out in the resting state so the block never shifts).
  const [projBlockHover, setProjBlockHover] = useState(false)

  // Right-click on the logo band inverts the band's colours: the background
  // takes the logo's accent colour and the logo takes the page background.
  const [logoInverted, setLogoInverted] = useState(false)
  useEffect(() => {
    if (!projMenuOpen) return
    const close = () => setProjMenuOpen(false)
    window.addEventListener('click', close)
    return () => window.removeEventListener('click', close)
  }, [projMenuOpen])

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
    padding: '0 24px 16px',
    display: 'flex',
    flexDirection: 'column',
    gap: 0,
  } : {
    width: sidebarWidth,
    minWidth: MIN_SIDEBAR_WIDTH,
    flexShrink: 0,
    position: 'relative',
    borderRight: '1px solid ' + dividerColor,
    boxShadow: dividerGlow,
    transition: glowTransition,
    padding: '0 16px 16px',
    display: 'flex',
    flexDirection: 'column',
    gap: 0,
  }

  // Mobile touch: bigger text and taller hit areas so the options are easy to
  // tap on a phone. Desktop keeps the compact sizes, nudged up one pixel from
  // the shared md scale at the user's request (sidebar text slightly larger).
  const optSize = isMobile ? fontSizes.xl : fontSizes.md + 1
  const optLineNum = isMobile ? 40 : 20
  const optLine = optLineNum + 'px'
  const optPad = isMobile ? '4px 0' : undefined
  // Horizontal content inset; mobile gets more breathing room on both sides.
  // The section dividers use a matching negative horizontal margin so they
  // stay edge-to-edge.
  // Two horizontal paddings: edgePadX = the root's horizontal padding, used
  // only by direct children (the logo band) so its border reaches the vertical
  // divider; contentPadX = the text inset inside the scroll area — elements in
  // there (hrs, hover rows) bleed by contentPadX to reach the container edge.
  const edgePadX = isMobile ? 24 : 16
  const contentPadX = isMobile ? 24 : 20
  const hrX = -contentPadX
  // Inter-row gap for the options list. Kept at 0 so the rows sit flush: with
  // the hover background bleeding full-width, neighbouring hovered rows read as
  // one continuous strip with no gap between their backgrounds. Vertical spacing
  // between text comes from each row's own padding instead.
  const optGap = 0
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
          A 12px strip whose grip sits centered on the divider (the root's
          borderRight). The divider line itself is drawn by the root, so it
          stays a single 1px line that matches the other dividers. */}
      {!isMobile && (
        <div
          onMouseDown={handleResizeStart}
          onDoubleClick={handleResizeReset}
          onMouseEnter={() => setResizeHover(true)}
          onMouseLeave={() => setResizeHover(false)}
          title={t('sidebar.resize.tooltip')}
          style={{ position: 'absolute', top: 0, bottom: 0, right: 0, width: 12, cursor: 'col-resize', zIndex: 5, userSelect: 'none' }}
        >
          {/* Grip centered on the divider (drawn by the root's borderRight),
              painted above it. right:-3 lands its center on the 1px border
              edge (the handle's containing block is the root's padding box,
              one pixel inside the border). */}
          <div style={{ position: 'absolute', top: '50%', right: -3, width: 5, height: 30, marginTop: -15, borderRadius: 3, background: resizeHover ? colors.textDim : colors.textDimmed, zIndex: 1, transition: 'background 0.15s' }} />
        </div>
      )}
      {/* Fixed header (close + logo + project) — never scrolls.
          Bleeds to the sidebar's edges (negating the root padding) so its
          overflow clips the logo exactly at the right divider instead of
          letting it paint over the main content area. */}
      <div
        onContextMenu={(e) => { e.preventDefault(); setLogoInverted(v => !v) }}
        title={t('sidebar.logo.tooltip')}
        style={{
          flexShrink: 0, overflow: 'hidden', margin: `0 -${edgePadX}px`, padding: `0 ${contentPadX}px`,
          borderBottom: '1px solid ' + colors.border, height: VIEW_HEADER_HEIGHT, display: 'flex', alignItems: 'center',
          background: logoInverted ? colors.accent : 'transparent', transition: 'background 0.2s ease',
        }}
      >
      {/* Logo + project name — clicking anywhere here plays the single-click
          cycle (a drag in the logo suppresses it). */}
      <div style={{ flex: 1, minWidth: 0 }} onClick={onLogoClick}>
        <h2 ref={logoWrapRef} style={{ margin: '0 0 0 -3px', lineHeight: 1, color: logoInverted ? colors.bg : colors.accent, fontSize: 36, fontFamily: "'SomeType Mono', 'Fira Mono', 'PT Mono', monospace", fontWeight: 'bold', transition: 'color 0.2s ease' }}>
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
      </div>
      </div>

      {/* Scrollable options between the fixed header and footer. The negative
          horizontal margin lets the section dividers span edge-to-edge (into
          the sidebar's padding), while the padding keeps the text inset. The
          flex column + gap reproduces the sidebar's original item spacing. */}
      <div style={{ flex: 1, minHeight: 0, overflowY: 'auto', overflowX: 'hidden', margin: `0 -${edgePadX}px`, padding: `0 ${contentPadX}px`, display: 'flex', flexDirection: 'column', gap: optGap }}>

      {/* View selector — the three agent views; absent in control mode (no
          project is attached, so talk/events/workers are unavailable) */}
      {mode === 'project' && (<>
      <strong style={{ marginTop: 16, marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>{t('sidebar.views')}</strong>
      {(Object.keys(VIEW_LABELS) as ViewMode[]).map((v) => (
        <div
          key={v}
          onClick={() => { setView(v); onNavigate() }}
          onMouseEnter={() => setHoverId('view:' + v)}
          onMouseLeave={() => setHoverId(null)}
          style={{ ...hoverStyle('view:' + v), cursor: 'pointer', color: view === v ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine, display: 'flex', alignItems: 'center' }}
        >
          <span style={{ flex: 1 }}>{VIEW_LABELS[v]}{view === v ? ' \u25C9' : ''}</span>
          {v === 'approvals' && pendingApprovals > 0 && (
            <span style={{ border: '1px solid ' + colors.accent, color: colors.accent, borderRadius: 2, minWidth: 14, textAlign: 'center', padding: '0 3px', fontSize: 10, lineHeight: '13px', userSelect: 'none' }}>{pendingApprovals}</span>
          )}
        </div>
      ))}

      {/* Worker selector list — hidden in the workers view (the main area IS
          the worker list there) and in the programs view (program browsing
          needs no worker filter). Its separator is hidden too, so switching to
          those views doesn't leave a stray line. */}
      {view !== 'workers' && view !== 'programs' && (
        <>
          <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px ' + hrX + 'px' }} />
          <strong style={{ display: 'block', color: colors.text, fontSize: fontSizes.xl, marginBottom: 8 }}>{t('sidebar.workerSelector')}</strong>
          {/* Control row: the compact multi-select tag filter takes 2/3 of the
              width, the expand (opens the picker modal) button takes 1/3.
              Expand shows text, not an icon. */}
          <div style={{ display: 'flex', gap: 8, alignItems: 'stretch', marginBottom: 11 }}>
            <div style={{ flex: '2 1 0', minWidth: 0 }}>
              <TagFilterDropdown
                fill
                tags={selectorTags}
                selected={filterTags}
                onToggle={(tag) => setFilterTags(prev => prev.includes(tag) ? prev.filter(t => t !== tag) : [...prev, tag])}
                onClear={() => setFilterTags([])}
                // Match the trigger to the sibling expand button (and the worker
                // rows below) so the whole selector reads at one size/height.
                triggerSize={optSize}
                triggerLine={optLine}
              />
            </div>
            {selectorWorkers.length > 1 && (
              <div style={{ flex: '1 1 0', minWidth: 0, display: 'flex' }}>
                <span
                  onClick={() => setShowWorkerPicker(true)}
                  title={t('sidebar.workerSelector.expand')}
                  className="btn-hover"
                  style={{ flex: 1, cursor: 'pointer', userSelect: 'none', display: 'inline-flex', alignItems: 'center', justifyContent: 'center', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', color: colors.textDim, border: '1px solid ' + colors.border, borderRadius: 3, fontSize: optSize, lineHeight: optLine, padding: '0 6px' }}
                >
                  {t('sidebar.workerSelector.expandShort')}
                </span>
              </div>
            )}
          </div>
          {shownSelectorWorkers.map((w) => {
            const isActive = view === 'talk'
              ? talkWorkers.has(w.id)
              : filterWorkers.has(w.id)
            const connection = w.online === false ? t('worker.offline') : t('worker.online')
            if (view === 'talk') {
              // Single line: every entry here is a reason worker, so the type
              // is redundant — just name + connection status.
              const hovered = hoverId === w.id
              return (
                <div
                  key={w.id}
                  onClick={() => handleWorkerClick(w.id)}
                  onMouseEnter={() => setHoverId(w.id)}
                  onMouseLeave={() => setHoverId(null)}
                  style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6, marginBottom: 4, minWidth: 0, padding: optPad }}
                >
                  <CheckBox active={isActive} accent={colors.accent} bg={colors.bg} border={colors.border} size={checkSize} style={{ marginTop: 2 }} />
                  <span
                    title={w.id}
                    style={{ color: isActive || hovered ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', flex: 1, minWidth: 0, transition: 'color 0.12s' }}
                  >
                    {w.id}
                  </span>
                  <span style={{ color: w.online === false ? colors.textDimmed : colors.toolCompleted, fontSize: fontSizes.sm, flexShrink: 0 }}>
                    {connection}
                  </span>
                </div>
              )
            }
            // Events view: single line — id with its type in brackets.
            const hovered = hoverId === w.id
            return (
              <div key={w.id} style={{ marginBottom: 6 }}>
                <div
                  onClick={() => handleWorkerClick(w.id)}
                  onMouseEnter={() => setHoverId(w.id)}
                  onMouseLeave={() => setHoverId(null)}
                  style={{ cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 6, minWidth: 0, padding: optPad }}
                >
                  <CheckBox active={isActive} accent={colors.accent} bg={colors.bg} border={colors.border} size={checkSize} style={{ marginTop: 2 }} />
                  <span style={{ display: 'flex', alignItems: 'baseline', gap: 12, flex: 1, minWidth: 0 }}>
                    <span
                      title={w.id}
                      style={{ color: isActive || hovered ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis', flexShrink: 1, minWidth: 0, transition: 'color 0.12s' }}
                    >
                      {w.id}
                    </span>
                    {w.type ? <span style={{ color: colors.textDimmed, fontSize: fontSizes.sm, flexShrink: 0 }}>[{w.type}]</span> : null}
                  </span>
                </div>
              </div>
            )
          })}

          {/* View Settings — talk view only */}
          {view === 'talk' && (
            <>
              <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px ' + hrX + 'px' }} />
              <strong style={{ marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>{t('sidebar.viewSettings')}</strong>
              <ToggleRow label={t('view.toggle.expandThinking')} on={viewSettings.thinkingExpanded} onToggle={() => onToggleViewSetting('thinkingExpanded')} colors={colors} dark={dark} mobile={isMobile} size={optSize} />
              <ToggleRow label={t('view.toggle.compactMode')} on={viewSettings.compactMode} onToggle={() => onToggleViewSetting('compactMode')} colors={colors} dark={dark} mobile={isMobile} size={optSize} />
              <ToggleRow label={t('view.toggle.streamingMode')} on={viewSettings.streamingMode} onToggle={() => onToggleViewSetting('streamingMode')} colors={colors} dark={dark} mobile={isMobile} size={optSize} />
              <ToggleRow label={t('view.toggle.responseOnly')} on={viewSettings.responseOnly} onToggle={() => onToggleViewSetting('responseOnly')} colors={colors} dark={dark} mobile={isMobile} size={optSize} />
            </>
          )}
        </>
      )}
      </>)}

      {/* Resources — the management category: project + template + provider +
          program browsing. For a project instance the first rows are a
          jump/start hop to other projects; in control mode this is the only
          usable section. In control mode no section precedes it, so the leading
          divider is skipped. */}
      {mode === 'project' && (
        <hr style={{ border: 'none', borderTop: '1px solid ' + colors.border, margin: '16px ' + hrX + 'px' }} />
      )}
      <strong style={{ marginTop: mode === 'project' ? 0 : 16, marginBottom: 8, color: colors.text, fontSize: fontSizes.xl }}>{t('sidebar.resources')}</strong>
      <div
        onClick={() => { onSelectPanel('projects'); onNavigate() }}
        onMouseEnter={() => setHoverId('projects')}
        onMouseLeave={() => setHoverId(null)}
        style={{ ...hoverStyle('projects'), cursor: 'pointer', color: panel === 'projects' ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine }}
      >
        {t('sidebar.projects')}{panel === 'projects' ? ' \u25C9' : ''}
      </div>
      <div
        onClick={() => { onSelectPanel('templates'); onNavigate() }}
        onMouseEnter={() => setHoverId('templates')}
        onMouseLeave={() => setHoverId(null)}
        style={{ ...hoverStyle('templates'), cursor: 'pointer', color: panel === 'templates' ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine }}
      >
        {t('sidebar.templates')}{panel === 'templates' ? ' \u25C9' : ''}
      </div>
      <div
        onClick={() => { onSelectPanel('providers'); onNavigate() }}
        onMouseEnter={() => setHoverId('providers')}
        onMouseLeave={() => setHoverId(null)}
        style={{ ...hoverStyle('providers'), cursor: 'pointer', color: panel === 'providers' ? colors.accent : colors.textDim, fontSize: optSize, lineHeight: optLine }}
      >
        {t('sidebar.providers')}{panel === 'providers' ? ' \u25C9' : ''}
      </div>

      </div>

      {/* Fixed footer: current project on the left, theme + language toggles
          on the right — stays put when the options scroll. The row reads as
          two blocks: hovering the project name lights up the left half only. */}
      <div style={{ flexShrink: 0, marginTop: 16, display: 'flex', alignItems: 'center', gap: 12 }}>
        {mode === 'project' && project && (
          <div style={{ flex: 1, minWidth: 0, position: 'relative', alignSelf: 'stretch', display: 'flex' }}>
            <div
              onClick={(e) => {
                e.stopPropagation()
                if (projRunning === undefined) return
                setProjMenuOpen(v => !v)
              }}
              onMouseEnter={() => setProjBlockHover(true)}
              onMouseLeave={() => setProjBlockHover(false)}
              title={projRunning === false ? t('projects.stopped') : project}
              className="btn-hover"
              style={{
                flex: 1, minWidth: 0, display: 'flex', alignItems: 'center',
                padding: '2px 8px', lineHeight: '20px',
                // Border appears on hover / while the menu is open; it is laid
                // out transparently otherwise so nothing shifts.
                border: '1px solid ' + (projBlockHover || projMenuOpen ? colors.border : 'transparent'),
                color: colors.textDim, fontSize: fontSizes.sm + 1,
                cursor: projRunning !== undefined ? 'pointer' : 'default', userSelect: 'none',
                // While the menu is open the block fuses with it: same panel
                // background — the block's own top border becomes the separator
                // between menu and handle.
                ...(projMenuOpen ? { background: colors.bgLight } : {}),
              }}
            >
              <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{project}</span>
            </div>
            {/* Lifecycle menu, opening upward from the footer. Mirrors the
                projects page rows: running → restart/stop, stopped → start,
                with a spinner while an op is in flight and inline errors. */}
            {projMenuOpen && projRunning !== undefined && (
              <div
                onClick={(e) => e.stopPropagation()}
                style={{
                  position: 'absolute', left: 0, right: 0, bottom: '100%', zIndex: 60,
                  background: colors.bgLight, border: '1px solid ' + colors.border,
                  borderBottom: 'none',
                  boxShadow: '0 4px 12px rgba(0,0,0,0.15)', padding: 8,
                  display: 'flex', flexDirection: 'column', gap: 4,
                }}
              >
                {projBusy ? (
                  <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: fontSizes.sm, color: colors.textDim, padding: '3px 8px' }}>
                    <span className="niq-spinner" style={{ width: 12, height: 12, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />
                    {projBusy === 'stop' ? t('projects.stopping') : t('projects.restarting')}
                  </span>
                ) : projRunning ? (
                  <>
                    <span onClick={onProjectRestart} className="btn-hover" style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, borderRadius: 2, padding: '3px 8px', userSelect: 'none' }}>
                      {t('projects.restart')}
                    </span>
                    <span onClick={onProjectStop} className="btn-hover" style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.toolFailed, borderRadius: 2, padding: '3px 8px', userSelect: 'none' }}>
                      {t('projects.stop')}
                    </span>
                  </>
                ) : (
                  <span onClick={onProjectStart} className="btn-hover" style={{ cursor: 'pointer', fontSize: fontSizes.sm, color: colors.accent, borderRadius: 2, padding: '3px 8px', display: 'inline-flex', alignItems: 'center', gap: 6, userSelect: 'none' }}>
                    {projStarting && <span className="niq-spinner" style={{ width: 12, height: 12, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />}
                    {projStarting ? t('projects.starting') : t('projects.start')}
                  </span>
                )}
                {projActionErr && (
                  <span style={{ color: colors.toolFailed, fontSize: fontSizes.xs, padding: '0 8px', wordBreak: 'break-all' }}>{projActionErr}</span>
                )}
              </div>
            )}
          </div>
        )}
        <div
          onClick={toggle}
          style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm + 1 }}
        >
          {dark ? t('theme.light') : t('theme.dark')}
        </div>
        <div
          onClick={() => setLang(lang === 'en' ? 'zh' : 'en')}
          title={t('sidebar.lang.tooltip')}
          style={{ cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm + 1, userSelect: 'none' }}
        >
          {lang === 'en' ? t('lang.zh') : t('lang.en')}
        </div>
      </div>
      </div>

      {/* Expanded worker picker: searchable, tag-grouped modal opened by the
          selector's expand button. It toggles the same selection as the inline
          checklist. */}
      {showWorkerPicker && (
        <WorkerPickerModal
          title={t('sidebar.workerSelector')}
          // The expanded view shows the full selectable set with its own search
          // + tag filter — it is the pimarily where you find and pick workers,
          // so it must not be pre-narrowed by the inline sidebar filter.
          workers={selectorWorkers}
          selected={selectorSelected}
          onToggle={(id) => { if (view === 'talk') onToggleWorker(id); else onToggleFilterWorker(id) }}
          onClose={() => setShowWorkerPicker(false)}
          isMobile={isMobile}
        />
      )}
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
function ToggleRow({ label, on, onToggle, colors, dark, mobile = false, size = fontSizes.sm }: { label: string; on: boolean; onToggle: () => void; colors: Palette; dark: boolean; mobile?: boolean; size?: number }) {
  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 10, marginBottom: mobile ? 14 : 8, padding: mobile ? '4px 0' : undefined }}>
      <span style={{ color: colors.textDim, fontSize: size }}>{label}</span>
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
            // Knob: soft off-white in light mode (not harsh pure white) and a
            // dark grey in dark mode so it sits on the track instead of glaring.
            background: dark ? '#3a3a3a' : '#ececec',
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
