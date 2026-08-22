import { useEffect, useState, useRef, useCallback, useMemo } from 'react'
import Sidebar from './views/Sidebar'
import EventRow from './views/EventRow'
import EventDetail from './views/EventDetail'
import TalkView from './views/TalkView'
import WorkersView from './views/WorkersView'
import WorkerDetail from './views/WorkerDetail'
import TalkInput from './components/TalkInput'
import ResizablePanel from './components/ResizablePanel'
import { useTheme, fontSizes } from './theme'
import { usePolling } from './hooks/usePolling'
import { sendInput, abortWorker, fetchWorkers, loadEventsBefore } from './services/api'
import type { EventPayload, ViewMode, WorkerInfo } from './types'

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

  // ── SSE: subscribe to the selected workers (backend filters) ──
  const sseKey = view === 'talk' ? 'talk-all'
    : view === 'events' ? 'events-' + [...filterWorkers].sort().join(',')
    : 'workers-none'

  useEffect(() => {
    // The workers view is driven by polling; it has no event stream.
    if (view === 'workers') return
    const params = new URLSearchParams()
    if (view === 'events') {
      for (const id of filterWorkers) params.append('worker', id)
      if (traceFilter) params.set('trace', traceFilter)
    }
    const url = `/api/stream?${params}`
    setEvents([])
    eventsRef.current = []
    setDeliveries({})
    deliveriesRef.current = {}
    setSelectedEventId(null)
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
  }, [sseKey, traceFilter, view])

  // ── Polling ──
  usePolling<WorkerInfo[]>('/api/workers', 5000, setWorkers)

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
    if (reasonWorkers.length > 0) {
      abortWorker(reasonWorkers[0].id)
    }
  }, [workers])

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
      listRef.current.scrollTo({ top: listRef.current.scrollHeight, behavior: 'smooth' })
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
        setView={setView}
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
      />

      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', position: 'relative' }}>
        {view === 'talk' ? (
          <>
            <TalkView
              events={events}
              talkWorkers={talkWorkers}
              onTraceClick={handleTraceClick}
              onLoadMore={loadMore}
              onMention={(id) => { setInput(prev => prev + '@' + id + ' '); setMentionKey(k => k + 1) }}
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
            />
          </>
        ) : view === 'workers' ? (
          <div style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
            <WorkersView
              workers={workers}
              selectedId={selectedWorkerId}
              onSelect={selectWorker}
              onOpenEvents={handleSelectWorker}
            />
            {selectedWorker && (
              <ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
                <WorkerDetail worker={selectedWorker} onClose={() => setSelectedWorkerId(null)} />
              </ResizablePanel>
            )}
          </div>
        ) : (
          <>
            <div style={{ marginTop: 24, marginBottom: 12, fontSize: fontSizes.xl, color: colors.text, padding: '0 24px' }}>
              Events
              {filterWorkers.size > 0 && <span> <strong style={{ color: colors.textMuted }}>[{[...filterWorkers].join(', ')}]</strong></span>}
              {traceFilter && <span> — trace <strong style={{ color: colors.textMuted }}>{traceFilter}</strong></span>}
              {traceFilter && (
                <span
                  onClick={clearTraceFilter}
                  style={{ cursor: 'pointer', color: colors.textDimmed, textDecoration: 'underline', marginLeft: 12, fontSize: fontSizes.sm }}
                >
                  Clear
                </span>
              )}
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