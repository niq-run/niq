import { useEffect, useState, useRef, useCallback, useMemo } from 'react'
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
import { usePolling } from './hooks/usePolling'
import { sendInput, abortWorker, fetchWorkers, loadEventsBefore, fetchContext, setApiBase, fetchArchived, setArchived as apiSetArchived } from './services/api'
import type { ContextInfo, EventPayload, ViewMode, WorkerInfo } from './types'

// Right-hand event detail panel: default 40% of the viewport, resizable by
// dragging its left edge, never narrower than this.
const DETAIL_MIN_WIDTH = 360
const detailDefaultWidth = () =>
  Math.max(DETAIL_MIN_WIDTH, Math.round((typeof window !== 'undefined' ? window.innerWidth : 1280) * 0.4))

export default function App() {
  const { dark, colors } = useTheme()

  // ── Theme sync ──
  useEffect(() => {
    document.documentElement.style.background = colors.bg
    document.body.style.background = colors.bg
    const root = document.getElementById('root')
    if (root) root.style.background = colors.bg
  }, [colors.bg])

  // ── State ──
  const [events, setEvents] = useState<EventPayload[]>([])
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
  // Talk view settings (moved from TalkView's header to the sidebar)
  const [thinkingExpanded, setThinkingExpanded] = useState(true)
  const [compactMode, setCompactMode] = useState(false)
  const [streamingMode, setStreamingMode] = useState(false)
  const [responseOnly, setResponseOnly] = useState(false)
  const [traceFilter, setTraceFilter] = useState('')

  const eventsRef = useRef<EventPayload[]>([])
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

  // Picking a View (talk/events/workers) leaves the management panels.
  const selectView = (v: ViewMode) => {
    setPanel(null)
    setView(v)
  }

  // ── SSE: subscribe to the selected workers (backend filters) ──
  const sseKey = view === 'talk' ? 'talk-all'
    : view === 'events' ? 'events-' + [...filterWorkers].sort().join(',')
    : 'workers-none'

  useEffect(() => {
    // No project → no event stream.
    if (mode !== 'project') return
    // A management panel is showing, not this project's events.
    if (panel) return
    // The workers view is driven by polling; it has no event stream.
    if (view === 'workers') return
    const params = new URLSearchParams()
    if (view === 'events') {
      for (const id of filterWorkers) params.append('worker', id)
      if (traceFilter) params.set('trace', traceFilter)
    }
    const url = projectBase + `/api/stream?${params}`
    setEvents([])
    eventsRef.current = []
    setDeliveries({})
    deliveriesRef.current = {}
    setSelectedEventId(null)
    if (view === 'events') eventsMountedAt.current = Date.now()
    const es = new EventSource(url)
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
      eventsRef.current = [...eventsRef.current.slice(-200), evt]
      setEvents(eventsRef.current)
    }
    return () => es.close()
  }, [sseKey, traceFilter, view, mode, projectBase, panel])

  // ── Polling (only meaningful when a project is attached). The URL is
  // prefixed with projectBase so in dev (?project=&port=) it hits the project's
  // own address, not the dev/control port.
  const workersURL = projectBase + '/api/workers'
  // Poll only while an agent view (not a management panel) is showing. The
  // immediate first load is required so the talk mention dropdown has the
  // worker list right away.
  usePolling<WorkerInfo[]>(workersURL, 5000, setWorkers, mode === 'project' && !panel)

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
      const older = await loadEventsBefore(oldestId, 50, workers, trace)
      if (older.length === 0) return
      const filtered = older.filter((e: any) => e.type !== 'event.delivered')
      if (filtered.length === 0) return
      const prepend = filtered.reverse()
      eventsRef.current = [...prepend, ...eventsRef.current]
      setEvents(eventsRef.current)
    } catch {}
  }, [events, view, filterWorkers, traceFilter])

  // ── Events list auto-scroll ──
  useEffect(() => {
    if (autoScrollRef.current && listRef.current) {
      // Instantly position after switching in / initial population (within the
      // first second); smooth-scroll only for later live updates.
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
        viewSettings={{ thinkingExpanded, compactMode, streamingMode, responseOnly }}
        onToggleViewSetting={(k) => {
          if (k === 'thinkingExpanded') setThinkingExpanded(v => !v)
          else if (k === 'compactMode') setCompactMode(v => !v)
          else if (k === 'streamingMode') setStreamingMode(v => !v)
          else setResponseOnly(v => !v)
        }}
        mode={mode}
        project={projectName}
        panel={panel}
        onSelectPanel={setPanel}
        archived={archived}
      />

      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', position: 'relative' }}>
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
              thinkingExpanded={thinkingExpanded}
              compactMode={compactMode}
              streamingMode={streamingMode}
              responseOnly={responseOnly}
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
              mentionKey={mentionKey}
              mentionTarget={mentionTarget}
              onClearMentionTarget={() => setMentionTarget('')}
              onSelectTarget={(id) => setMentionTarget(id)}
            />
          </>
        ) : view === 'workers' ? (
          <div style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
            <WorkersView
              workers={workers}
              archived={archived}
              onToggleArchived={toggleArchived}
              selectedId={selectedWorkerId}
              onSelect={selectWorker}
              onOpenEvents={handleSelectWorker}
            />
            {selectedWorker && (
              <ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
                <WorkerDetail
                  worker={selectedWorker}
                  onClose={() => setSelectedWorkerId(null)}
                  archived={archived}
                  onToggleArchived={toggleArchived}
                />
              </ResizablePanel>
            )}
          </div>
        ) : (
          <>
            <div style={{ marginTop: 24, marginBottom: 12, fontSize: fontSizes.xl, color: colors.text, padding: '0 24px', display: 'flex', alignItems: 'baseline', gap: 16 }}>
              <strong>Events</strong>
              <span style={{ fontSize: fontSizes.sm, color: colors.textMuted }}>
                {filterWorkers.size > 0 && (
                  <>
                    filtering <strong style={{ color: colors.textDim }}>[{[...filterWorkers].join(', ')}]</strong>
                    {traceFilter && ' · '}
                  </>
                )}
                {traceFilter && (
                  <>
                    trace <strong style={{ color: colors.textDim }}>{traceFilter}</strong>
                  </>
                )}
                {filterWorkers.size === 0 && !traceFilter && (
                  <strong style={{ color: colors.textDim }}>[all workers]</strong>
                )}
                {traceFilter && (
                  <span
                    onClick={clearTraceFilter}
                    style={{ cursor: 'pointer', color: colors.textDimmed, textDecoration: 'underline', marginLeft: 12, fontSize: fontSizes.sm }}
                  >
                    Clear
                  </span>
                )}
              </span>
            </div>

            <div style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
              <div ref={listRef} onScroll={handleScroll} style={{ flex: 1, minWidth: 0, overflowY: 'auto', fontSize: fontSizes.md, padding: '0 24px 16px 24px' }}>
                {events.length > 0 && <div ref={sentinelRef} style={{ height: 1 }} />}
                <table style={{ width: '100%', borderCollapse: 'collapse', tableLayout: 'fixed', fontSize: fontSizes.md }}>
                  <thead>
                    <tr style={{ textAlign: 'left', color: colors.textDimmed, fontSize: fontSizes.xs }}>
                      <th style={{ padding: '6px 6px', width: 80, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="event timestamp">Time</th>
                      <th style={{ padding: '6px 6px', width: 180, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="event type">Type</th>
                      <th style={{ padding: '6px 6px', width: 240, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="worker that published the event">Worker ID</th>
                      <th style={{ padding: '6px 6px', width: 120, position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="who received the event (delivered to)">Reception</th>
                      <th style={{ padding: '6px 6px', position: 'sticky', top: 0, background: colors.bg, zIndex: 1, boxShadow: 'inset 0 -1px 0 ' + colors.border }} title="summary of the event payload">Content</th>
                    </tr>
                  </thead>
                  <tbody>
                    {events.map((evt) => (
                      <EventRow key={evt.id} evt={evt} selected={evt.id === selectedEventId} onSelect={() => selectEvent(evt.id)} onOpenWorker={handleSelectWorker} deliveries={deliveries} workerTypes={workerTypes} />
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
            {/* Detail panel anchored to the page-level column, so it spans the
                full height including the Events title — same as the workers view. */}
            {selectedEvent && (
              <ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
                <EventDetail evt={selectedEvent} deliveries={deliveries} onClose={() => setSelectedEventId(null)} />
              </ResizablePanel>
            )}
          </>
        )}
      </div>
    </div>
  )
}