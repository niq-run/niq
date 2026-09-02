import { useEffect, useState, useRef, useCallback, useMemo, useLayoutEffect, type ReactNode } from 'react'
import Sidebar from './views/Sidebar'
import EventRow from './views/EventRow'
import EventDetail from './views/EventDetail'
import TalkView from './views/TalkView'
import WorkersView from './views/WorkersView'
import WorkerDetail from './views/WorkerDetail'
import ProjectsView from './views/ProjectsView'
import TemplatesView from './views/TemplatesView'
import TalkInput from './components/TalkInput'
import ResizablePanel from './components/ResizablePanel'
import { useTheme, fontSizes } from './theme'
import { useI18n } from './i18n'
import { usePolling } from './hooks/usePolling'
import { useIsMobile } from './hooks/useIsMobile'
import { sendInput, abortWorker, fetchWorkers, loadEventsBefore, fetchContext, setApiBase, fetchArchived, setArchived as apiSetArchived } from './services/api'
import type { ContextInfo, EventPayload, ViewMode, ViewSettings, ViewSettingKey, WorkerInfo } from './types'

// Talk view settings are persisted to localStorage so toggles survive reloads.
const VIEW_SETTINGS_KEY = 'niq.view-settings'
const DEFAULT_VIEW_SETTINGS: ViewSettings = {
  thinkingExpanded: true,
  compactMode: false,
  streamingMode: false,
  responseOnly: false,
}
function loadViewSettings(): ViewSettings {
  try {
    const raw = localStorage.getItem(VIEW_SETTINGS_KEY)
    if (raw) return { ...DEFAULT_VIEW_SETTINGS, ...JSON.parse(raw) }
  } catch { /* fall through to defaults */ }
  return DEFAULT_VIEW_SETTINGS
}

// Right-hand event detail panel: default 40% of the viewport, resizable by
// dragging its left edge, never narrower than this.
const DETAIL_MIN_WIDTH = 360
const detailDefaultWidth = () =>
  Math.max(DETAIL_MIN_WIDTH, Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.4))

// Mobile detail overlay: full screen width below the top bar, right-anchored.
// top matches the top bar's box-border height so the panel starts exactly at
// the bar's bottom border line.
const MOBILE_TOP_BAR_HEIGHT = 44
function MobileDetailPanel({ children }: { children: ReactNode }) {
  const { colors } = useTheme()
  return (
    <div style={{ position: 'fixed', top: MOBILE_TOP_BAR_HEIGHT, right: 0, bottom: 0, width: '100%', zIndex: 20, display: 'flex', background: colors.bg }}>
      {children}
    </div>
  )
}

// Merge incoming events into an existing list: dedupe by id and sort by
// timestamp (with an id tiebreak). Used for both live appends and history
// prepends so the timeline is independent of delivery order.
function mergeEvents(existing: EventPayload[], incoming: EventPayload[]): EventPayload[] {
  const seen = new Set(existing.map((e) => e.id))
  const out = existing.slice()
  for (const e of incoming) {
    if (!seen.has(e.id)) {
      seen.add(e.id)
      out.push(e)
    }
  }
  out.sort((a, b) => a.timestamp - b.timestamp || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  return out
}

export default function App() {
  const { dark, colors } = useTheme()
  const { t } = useI18n()
  const isMobile = useIsMobile()
  const [sidebarOpen, setSidebarOpen] = useState(false)

  // ── Theme sync ──
  useEffect(() => {
    document.documentElement.style.background = colors.bg
    document.body.style.background = colors.bg
    const root = document.getElementById('root')
    if (root) root.style.background = colors.bg
  }, [colors.bg])

  // ── State ──
  const [events, setEvents] = useState<EventPayload[]>([])
  // True while (re)subscribing and loading history after a disconnect/reconnect.
  const [reloading, setReloading] = useState(false)
  const [workers, setWorkers] = useState<WorkerInfo[]>([])
  const [view, setView] = useState<ViewMode>('talk')
  const [context, setContext] = useState<ContextInfo>({ mode: 'project' })
  const [panel, setPanel] = useState<'projects' | 'templates' | null>(null)
  const [archived, setArchived] = useState<Set<string>>(new Set())
  const [input, setInput] = useState('')
  const [inputMode, setInputMode] = useState('default')
  const [sending, setSending] = useState(false)
  const [mentionKey, setMentionKey] = useState(0)
  const [filterWorkers, setFilterWorkers] = useState<Set<string>>(new Set())
  const [selectedEventId, setSelectedEventId] = useState<string | null>(null)
  const [selectedWorkerId, setSelectedWorkerId] = useState<string | null>(null)
  const [detailWidth, setDetailWidth] = useState<number>(detailDefaultWidth)
  const detailDraggedRef = useRef(false)
  const [deliveries, setDeliveries] = useState<Record<string, string[]>>({})
  const [talkWorkers, setTalkWorkers] = useState<Set<string>>(new Set())
  const [mentionTarget, setMentionTarget] = useState('')
  // Talk view settings (moved from TalkView's header to the sidebar), persisted
  // to localStorage so they survive reloads / new sessions.
  const [viewSettings, setViewSettings] = useState<ViewSettings>(loadViewSettings)
  const toggleViewSetting = (k: ViewSettingKey) => {
    setViewSettings(vs => {
      const next = { ...vs, [k]: !vs[k] }
      try { localStorage.setItem(VIEW_SETTINGS_KEY, JSON.stringify(next)) } catch { /* ignore */ }
      return next
    })
  }
  const [traceFilter, setTraceFilter] = useState('')

  const eventsRef = useRef<EventPayload[]>([])
  const seenRef = useRef<Set<string>>(new Set())
  const deliveriesRef = useRef<Record<string, string[]>>({})
  const listRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)
  const sentinelRef = useRef<HTMLDivElement>(null)
  // When the events view (re)mounts / reconnects, the initial population should
  // land at the bottom instantly; only later live updates smooth-scroll.
  const eventsMountedAt = useRef(0)

  // ── Mode: control (no project attached) vs project. In control mode only the
  // projects surface is usable; talk/events/workers need an attached project, so
  // they are hidden and their SSE + polling are disabled. The projects API lives
  // on the control plane — same-origin in control mode, via context.control_url
  // from a project instance.
  // A query-string ?project=<id>&port=<port> names a specific running project to
  // develop against, so the whole UI talks to that project's own address (and
  // can keep several projects open at once — the control API still goes to 9527).
  const urlParams = typeof window !== 'undefined' ? new URLSearchParams(window.location.search) : new URLSearchParams()
  const urlPort = urlParams.get('port')
  const urlProject = urlParams.get('project')
  const devProjectBase = urlPort ? 'http://127.0.0.1:' + urlPort : ''

  const mode = devProjectBase !== '' ? 'project' : (context.mode === 'control' ? 'control' : 'project')
  const projectBase = devProjectBase
  // In dev (?project=&port=) we skip /api/context, so take the project name
  // from the URL; otherwise it comes from the served webui's context.
  const projectName = devProjectBase !== '' ? (urlProject || '') : context.project

  // Page title: append the attached project name when there is one.
  useEffect(() => {
    document.title = projectName ? `niq · ${projectName}` : 'niq'
  }, [projectName])

  useEffect(() => {
    setApiBase(projectBase)
    // Unless a project+port was given in the URL, ask /api/context to learn the
    // mode (dev proxy → 9527 control, or a served project webui answers locally).
    if (devProjectBase === '') {
      fetchContext().then(setContext).catch(() => {})
    }
  }, [projectBase, devProjectBase])

  // Archived workers: hidden from the worker selector by default; toggled from
  // the workers view. State lives in the project's stream definitions.
  useEffect(() => {
    if (mode !== 'project') { setArchived(new Set()); return }
    fetchArchived()
      .then((list) => setArchived(new Set(list)))
      .catch(() => setArchived(new Set()))
  }, [projectBase, mode])

  const toggleArchived = useCallback(async (id: string) => {
    const next = !archived.has(id)
    try {
      const list = await apiSetArchived(id, next)
      setArchived(new Set(list))
    } catch {}
  }, [archived])

  // Picking a View (talk/events/workers) leaves the management panels and
  // closes the mobile drawer.
  const selectView = (v: ViewMode) => {
    setPanel(null)
    setView(v)
    setSidebarOpen(false)
  }

  // ── SSE stream key ──
  // The SSE is a *project-level* subscription to the event stream — it is not
  // owned by any view. It only reconnects when the stream's filter actually
  // changes, which happens solely for the events view (worker/trace filter).
  // talk/workers (and any other non-events view) all use the same default
  // stream, so switching between them never tears the connection down — the
  // previous view's SSE simply stays alive. `view` is intentionally excluded
  // from the effect deps for this reason.
  const streamKey = view === 'events'
    ? 'events-' + [...filterWorkers].sort().join(',') + '-' + traceFilter
    : 'all'

  useEffect(() => {
    // No project → no event stream.
    if (mode !== 'project') return
    // The SSE stays up across views AND management panels: tearing it down on
    // a panel switch would race a rebuild against a message sent right after
    // returning to talk, and its live events could be lost. The cost of one
    // idle connection is negligible.
    const params = new URLSearchParams()
    if (view === 'events') {
      for (const id of filterWorkers) params.append('worker', id)
      if (traceFilter) params.set('trace', traceFilter)
    }
    const url = projectBase + `/api/stream?${params}`

    // The events view clears its timeline immediately on entry (worker/trace
    // filter changes the scope) so no stale rows flash before history arrives.
    if (view === 'events') {
      setEvents([])
      eventsRef.current = []
      seenRef.current.clear()
      setDeliveries({})
      deliveriesRef.current = {}
      eventsMountedAt.current = Date.now()
    }
    setSelectedEventId(null)

    // Page backwards from the watermark to fill history. Issued once the stream
    // advertises its watermark. This is treated as a fresh (re)subscription: we
    // discard any cached timeline and rebuild from the watermark, so a reconnect
    // (network drop, or entering the filtered events view) always produces a
    // clean, correctly-ordered timeline — no merge-across-caches gymnastics.
    const loadInitialHistory = async (watermark: string) => {
      if (!watermark) return
      const limit = view === 'events' ? 20 : 100
      const workers = view === 'events' ? [...filterWorkers] : []
      const trace = view === 'events' ? traceFilter : ''
      setReloading(true)
      try {
        eventsRef.current = []
        seenRef.current.clear()
        deliveriesRef.current = {}
        setDeliveries({})
        const older = (await loadEventsBefore(watermark, limit, workers, trace)) as EventPayload[]
        const filtered = older.filter((e) => e.type !== 'event.delivered')
        const merged = mergeEvents(eventsRef.current, filtered)
        eventsRef.current = merged
        setEvents(merged)
      } catch {}
      setReloading(false)
    }

    const es = new EventSource(url)
    // The server advertises the subscription watermark as a control event before
    // any data; we use it to kick off backwards pagination for history.
    const onWatermark = (e: MessageEvent) => loadInitialHistory(e.data as string)
    es.addEventListener('watermark', onWatermark)
    es.onmessage = (msg) => {
      const evt = JSON.parse(msg.data) as EventPayload
      if (evt.type === 'event.delivered') {
        const eventId = evt.payload?.event_id as string | undefined
        const recipients = evt.payload?.recipients as string[] | undefined
        if (eventId && recipients) {
          deliveriesRef.current = { ...deliveriesRef.current, [eventId]: recipients }
          setDeliveries(deliveriesRef.current)
        }
        return
      }
      if (seenRef.current.has(evt.id)) return
      seenRef.current.add(evt.id)
      // Sort by timestamp (id tiebreak) rather than arrival order, so any
      // out-of-order delivery from the live stream can't scramble the tail.
      const next = mergeEvents(eventsRef.current, [evt])
      eventsRef.current = next
      setEvents(next)
    }
    return () => es.close()
    // `view` intentionally excluded: talk↔workers must keep the same connection.
    // `streamKey` already encodes the events-view filter, so traceFilter is redundant here.
  }, [streamKey, mode, projectBase])

  // ── Polling (only meaningful when a project is attached). The URL is
  // prefixed with projectBase so in dev (?project=&port=) it hits the project's
  // own address, not the dev/control port.
  const workersURL = projectBase + '/api/workers'
  // Poll only while an agent view (not a management panel) is showing. The
  // immediate first load is required so the talk mention dropdown has the
  // worker list right away.
  usePolling<WorkerInfo[]>(workersURL, 5000, setWorkers, mode === 'project' && !panel)

  // Manual refresh: re-fetch the worker list immediately, so a worker just
  // declared in project.json appears without waiting for the next poll.
  const refreshWorkers = useCallback(async () => {
    try {
      const res = await fetch(workersURL)
      if (res.ok) setWorkers(await res.json())
    } catch { /* next poll retries */ }
  }, [workersURL])

  // ── Callbacks ──
  const sendMessage = useCallback(() => {
    if (!input.trim() || sending) return
    setSending(true)
    // Parse @mention for targeting a specific worker.
    let msgTarget = ''
    let msgText = input
    const mentionMatch = input.match(/^@(\S+)\s+(.*)$/s)
    if (mentionMatch) {
      const mentioned = mentionMatch[1]
      const reasonWorkers = workers.filter(w => w.type === 'reason')
      if (reasonWorkers.some(r => r.id === mentioned)) {
        msgTarget = mentioned
        msgText = mentionMatch[2]
        setMentionTarget(mentioned) // persist the @ target for the next message
      }
    }
    if (!msgTarget) {
      // No @mention: reuse the persisted target if still valid; otherwise the
      // first selected reason worker, or broadcast.
      const reasonWorkers = workers.filter(w => w.type === 'reason')
      if (mentionTarget && reasonWorkers.some(r => r.id === mentionTarget)) {
        msgTarget = mentionTarget
      } else {
        const selectedReasons = [...talkWorkers].filter(id => reasonWorkers.some(r => r.id === id))
        msgTarget = selectedReasons.length > 0 ? selectedReasons[0] : ''
      }
    }
    sendInput(msgText, msgTarget, inputMode).then(() => {
      setInput('')
      setSending(false)
    }).catch(() => {
      setSending(false)
    })
  }, [input, view, talkWorkers, inputMode, sending, workers, mentionTarget])

  const handleAbort = useCallback(() => {
    const reasonWorkers = workers.filter(w => w.type === 'reason')
    const isReason = (id: string) => reasonWorkers.some(r => r.id === id)
    // Abort the worker the input is currently @-targeted at, falling back to a
    // selected reason worker, then the first reason worker.
    let target = isReason(mentionTarget) ? mentionTarget : ''
    if (!target) {
      const selected = [...talkWorkers].find(isReason)
      target = selected ?? (reasonWorkers.length > 0 ? reasonWorkers[0].id : '')
    }
    if (target) {
      abortWorker(target)
    }
  }, [workers, mentionTarget, talkWorkers])

  const toggleWorker = useCallback((id: string) => {
    setTalkWorkers(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  const selectEvent = useCallback((id: string) => {
    setSelectedEventId(prev => prev === id ? null : id)
  }, [])

  const selectWorker = useCallback((id: string) => {
    setSelectedWorkerId(prev => prev === id ? null : id)
  }, [])

  // ResizablePanel reports width changes during drag; mark as dragged so the
  // 40%-of-viewport resize-follow stops applying.
  const handlePanelResize = useCallback((w: number) => {
    detailDraggedRef.current = true
    setDetailWidth(w)
  }, [])

  // Keep the panel at 40% of the viewport on window resize, until the user
  // drags the divider (then it holds the dragged width).
  useEffect(() => {
    const onResize = () => {
      if (!detailDraggedRef.current) setDetailWidth(detailDefaultWidth())
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const handleTraceClick = useCallback((traceId: string) => {
    setTraceFilter(traceId)
    setView('events')
  }, [])

  const clearTraceFilter = useCallback(() => {
    setTraceFilter('')
  }, [])

  // Toggle a worker in the events-view filter set (multi-select).
  const toggleFilterWorker = useCallback((id: string) => {
    setFilterWorkers(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }, [])

  // Jump from a worker list / worker ID link into that worker's event stream.
  const handleSelectWorker = useCallback((id: string) => {
    setFilterWorkers(new Set([id]))
    setView('events')
  }, [])

  // ── Load more older events ──
  const loadMore = useCallback(async () => {
    if (events.length === 0) return
    const oldestId = events[0].id
    const workers = view === 'events' ? [...filterWorkers] : []
    const trace = view === 'events' ? traceFilter : ''
    try {
      const older = (await loadEventsBefore(oldestId, 20, workers, trace)) as EventPayload[]
      if (older.length === 0) return
      const filtered = older.filter((e) => e.type !== 'event.delivered')
      if (filtered.length === 0) return
      const merged = mergeEvents(eventsRef.current, filtered)
      eventsRef.current = merged
      setEvents(merged)
    } catch {}
  }, [events, view, filterWorkers, traceFilter])

  // ── Events list auto-scroll ──
  // Layout effect (before paint) so the initial load lands directly at the
  // bottom instead of painting the top first. Smooth-scroll only for later
  // live updates; the first ~1s after mount is instant.
  useLayoutEffect(() => {
    if (autoScrollRef.current && listRef.current) {
      const animated = Date.now() - eventsMountedAt.current > 1000
      listRef.current.scrollTo({ top: listRef.current.scrollHeight, behavior: animated ? 'smooth' : 'auto' })
    }
  }, [events])

  // Auto-load more events when scrolling to top.
  useEffect(() => {
    if (view !== 'events' || events.length === 0) return
    const el = sentinelRef.current
    if (!el) return
    const observer = new IntersectionObserver((entries) => {
      if (entries[0].isIntersecting) {
        loadMore()
      }
    }, { rootMargin: '200px 0px' })
    observer.observe(el)
    return () => observer.disconnect()
  }, [view, events.length, loadMore])

  const handleScroll = useCallback(() => {
    const el = listRef.current
    if (!el) return
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 50
    autoScrollRef.current = atBottom
  }, [])

  const workerTypes = useMemo(() => {
    const map: Record<string, string> = {}
    for (const w of workers) {
      map[w.id] = w.type
    }
    return map
  }, [workers])

  // Event currently shown in the right-hand detail panel (events view only).
  const selectedEvent = view === 'events' ? events.find(e => e.id === selectedEventId) : undefined
  // Worker currently shown in the right-hand detail panel (workers view only).
  const selectedWorker = view === 'workers' ? workers.find(w => w.id === selectedWorkerId) : undefined

  // ── Render ──
  return (
    <div data-theme={dark ? 'dark' : 'light'} style={{ display: 'flex', height: '100vh', fontFamily: 'monospace', color: colors.text, background: colors.bg }}>
      <Sidebar
        view={view}
        setView={selectView}
        filterWorkers={filterWorkers}
        onToggleFilterWorker={toggleFilterWorker}
        workers={workers}
        talkWorkers={talkWorkers}
        onToggleWorker={toggleWorker}
        viewSettings={viewSettings}
        onToggleViewSetting={toggleViewSetting}
        mode={mode}
        project={projectName}
        panel={panel}
        onSelectPanel={setPanel}
        archived={archived}
        isMobile={isMobile}
        open={sidebarOpen}
        onNavigate={() => setSidebarOpen(false)}
      />

      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', position: 'relative' }}>
        {/* Mobile top bar: hamburger opens the sidebar drawer, label shows the
            current view. Absent on desktop where the sidebar is always visible.
            Fixed border-box height keeps the detail overlay's top aligned to
            its bottom border. */}
        {isMobile && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: MOBILE_TOP_BAR_HEIGHT, boxSizing: 'border-box', padding: '0 16px', borderBottom: '1px solid ' + colors.border, flexShrink: 0, background: colors.bg, zIndex: 10 }}>
            <button
              onClick={() => setSidebarOpen(true)}
              title={t('app.menu')}
              style={{ background: 'none', border: '1px solid ' + colors.border, borderRadius: 4, padding: '5px 8px', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}
            >
              {/* Three drawn bars — the ☰ glyph is not vertically centered in
                  the monospace font, so draw it with real lines instead. */}
              <span style={{ display: 'flex', flexDirection: 'column', gap: 3 }}>
                <span style={{ width: 14, height: 2, background: colors.textDim, borderRadius: 1 }} />
                <span style={{ width: 14, height: 2, background: colors.textDim, borderRadius: 1 }} />
                <span style={{ width: 14, height: 2, background: colors.textDim, borderRadius: 1 }} />
              </span>
            </button>
            <strong style={{ fontSize: fontSizes.md, color: colors.text }}>
              {panel === 'templates' ? t('sidebar.templates')
                : panel === 'projects' ? t('sidebar.projects')
                : mode !== 'project' ? t('sidebar.projects')
                : view === 'talk' ? t('nav.talk')
                : view === 'events' ? t('nav.events')
                : t('nav.workers')}
            </strong>
          </div>
        )}
        {reloading && (
          <div style={{ position: 'fixed', top: 60, left: 0, right: 0, display: 'flex', justifyContent: 'center', pointerEvents: 'none', zIndex: 50 }}>
              <div style={{ background: colors.accentDim, color: colors.text, fontSize: fontSizes.sm, padding: '4px 14px', borderRadius: 4, opacity: 0.95, boxShadow: '0 2px 8px rgba(0,0,0,0.25)' }}>
              {t('app.loading')}
            </div>
          </div>
        )}
        {mode !== 'project' ? (
          panel === 'templates' ? <TemplatesView /> : <ProjectsView />
        ) : panel === 'templates' ? (
          <TemplatesView />
        ) : panel === 'projects' ? (
          <ProjectsView />
        ) : view === 'talk' ? (
          <>
            <TalkView
              events={events}
              talkWorkers={talkWorkers}
              onTraceClick={handleTraceClick}
              onLoadMore={loadMore}
              onMention={(id) => { setInput(prev => prev + '@' + id + ' '); setMentionTarget(id); setMentionKey(k => k + 1) }}
              deliveries={deliveries}
              workerTypes={workerTypes}
              thinkingExpanded={viewSettings.thinkingExpanded}
              compactMode={viewSettings.compactMode}
              streamingMode={viewSettings.streamingMode}
              responseOnly={viewSettings.responseOnly}
              isMobile={isMobile}
            />

            <TalkInput
              talkPartner={''}
              input={input}
              inputMode={inputMode}
              onInputChange={setInput}
              onSend={sendMessage}
              onAbort={handleAbort}
              onModeChange={setInputMode}
              workers={workers}
              archived={archived}
              mentionKey={mentionKey}
              mentionTarget={mentionTarget}
              onClearMentionTarget={() => setMentionTarget('')}
              onSelectTarget={(id) => setMentionTarget(id)}
              isMobile={isMobile}
            />
          </>
        ) : view === 'workers' ? (
          <div style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
            <WorkersView
              workers={workers}
              archived={archived}
              selectedId={selectedWorkerId}
              onSelect={selectWorker}
              onOpenEvents={handleSelectWorker}
              isMobile={isMobile}
              onRefresh={refreshWorkers}
            />
            {selectedWorker && (
              isMobile ? (
                <MobileDetailPanel>
                  					<WorkerDetail
                  						worker={selectedWorker}
                  						allWorkers={workers}
                  						onClose={() => setSelectedWorkerId(null)}
                  						archived={archived}
                  						onToggleArchived={toggleArchived}
                  						onDeleted={(id) => { setSelectedWorkerId(null); refreshWorkers() }}
                  					/>
                  				</MobileDetailPanel>
                  			  ) : (
                  				<ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
                  					<WorkerDetail
                  						worker={selectedWorker}
                  						allWorkers={workers}
                  						onClose={() => setSelectedWorkerId(null)}
                  						archived={archived}
                  						onToggleArchived={toggleArchived}
                  						onDeleted={(id) => { setSelectedWorkerId(null); refreshWorkers() }}
                  					/>
                </ResizablePanel>
              )
            )}
          </div>
        ) : (
          <>
            {/* On mobile the app-level top bar heads the page, so the in-view
                header is skipped (same as the talk view). */}
            {!isMobile && (
              <div style={{ marginTop: 24, marginBottom: 12, fontSize: fontSizes.xl, color: colors.text, padding: '0 24px', display: 'flex', alignItems: 'baseline', gap: 16 }}>
                <strong>{t('nav.events')}</strong>
                <span style={{ fontSize: fontSizes.sm, color: colors.textMuted }}>
                  {filterWorkers.size > 0 && (
                    <>
                      {t('events.filtering')} <strong style={{ color: colors.textDim }}>[{[...filterWorkers].join(', ')}]</strong>
                      {traceFilter && ' · '}
                    </>
                  )}
                  {traceFilter && (
                    <>
                      {t('events.trace')} <strong style={{ color: colors.textDim }}>{traceFilter}</strong>
                    </>
                  )}
                  {filterWorkers.size === 0 && !traceFilter && (
                    <strong style={{ color: colors.textDim }}>{t('events.allWorkers')}</strong>
                  )}
                  {traceFilter && (
                    <span
                      onClick={clearTraceFilter}
                      style={{ cursor: 'pointer', color: colors.textDimmed, textDecoration: 'underline', marginLeft: 12, fontSize: fontSizes.sm }}
                    >
                      {t('app.clear')}
                    </span>
                  )}
                </span>
              </div>
            )}

            <div style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
              <div ref={listRef} onScroll={handleScroll} style={{ flex: 1, minWidth: 0, overflow: 'auto', fontSize: fontSizes.md, padding: '0 24px 16px 24px' }}>
                {events.length > 0 && <div ref={sentinelRef} style={{ height: 1 }} />}
                {/* min-width lets the wide fixed columns scroll horizontally on
                    narrow (phone) viewports instead of collapsing. */}
                <table style={{ width: '100%', minWidth: 680, borderCollapse: 'collapse', tableLayout: 'fixed', fontSize: fontSizes.md }}>
                  <thead>
                    <tr style={{ textAlign: 'left', color: colors.textDimmed, fontSize: fontSizes.xs }}>
                      <th style={{ padding: '6px 6px', width: 80, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('events.col.time.tooltip')}>{t('events.col.time')}</th>
                      <th style={{ padding: '6px 6px', width: 180, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('events.col.type.tooltip')}>{t('events.col.type')}</th>
                      <th style={{ padding: '6px 6px', width: 240, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('events.col.workerId.tooltip')}>{t('events.col.workerId')}</th>
                      <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('events.col.reception.tooltip')}>{t('events.col.reception')}</th>
                      <th style={{ padding: '6px 6px', position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title={t('events.col.content.tooltip')}>{t('events.col.content')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((evt) => (
                      <EventRow key={evt.id} evt={evt} selected={evt.id === selectedEventId} onSelect={() => selectEvent(evt.id)} onOpenWorker={handleSelectWorker} deliveries={deliveries} workerTypes={workerTypes} isMobile={isMobile} />
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
            {/* Detail panel anchored to the page-level column, so it spans the
                full height including the Events title — same as the workers view. */}
            {selectedEvent && (
              isMobile ? (
                <MobileDetailPanel>
                  <EventDetail evt={selectedEvent} deliveries={deliveries} onClose={() => setSelectedEventId(null)} />
                </MobileDetailPanel>
              ) : (
                <ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
                  <EventDetail evt={selectedEvent} deliveries={deliveries} onClose={() => setSelectedEventId(null)} />
                </ResizablePanel>
              )
            )}
          </>
        )}
      </div>
    </div>
  )
}