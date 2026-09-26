import { useEffect, useState, useRef, useCallback, useMemo, useLayoutEffect, type ReactNode } from 'react'
import Sidebar from './views/Sidebar'
import EventRow from './views/EventRow'
import EventDetail from './views/EventDetail'
import TalkView from './views/TalkView'
import WorkersView from './views/WorkersView'
import WorkerDetail from './views/WorkerDetail'
import ProjectsView from './views/ProjectsView'
import TemplatesView from './views/TemplatesView'
import ProvidersView from './views/ProvidersView'
import ProgramsView from './views/ProgramsView'
import ViewHeader from './components/ViewHeader'
import ApprovalsView from './views/ApprovalsView'
import TalkInput from './components/TalkInput'
import ResizablePanel from './components/ResizablePanel'
import { useTheme, fontSizes } from './theme'
import { useI18n } from './i18n'
import { usePolling } from './hooks/usePolling'
import { useIsMobile } from './hooks/useIsMobile'
import { CONTROL } from './services/api'
import { sendInput, abortWorker, fetchWorkers, loadEventsBefore, fetchEventsByRequest, fetchContext, getApiBase, fetchArchived, setArchived as apiSetArchived, fetchApprovals, decideApproval, startProject, stopProject, restartProject, sendWorkerEvent } from './services/api'
import { attachmentBlock } from './components/talk-utils'
import { isTalkPartnerType, type ApprovalEntry, type ContextInfo, type EventPayload, type ProjectInfo, type StagedAttachment, type ViewMode, type ViewSettings, type ViewSettingKey, type WatchEntry, type WorkerInfo } from './types'

// How many history events the WebUI pages back per /api/events/before call
// (both the initial watermark backfill and the load-more pagination).
const HISTORY_PAGE = 100

// Thresholds for the talk/events list trim. Rather than trimming to a tiny
// window on every event (which made the pinned bottom visibly bounce as rows
// were removed/re-added), we let the live list grow up to TRIM_HIGH while the
// user follows, then trim once down to TRIM_LOW. That makes trims rare and big
// instead of constant and small. Trims only ever run while following (so a
// scrolled-up reader is never shifted); a trim is also triggered when the input
// box gains focus, so typing never contends with a huge DOM.
const TRIM_HIGH = 50
const TRIM_LOW = 50

// Per-reason-worker input mode, persisted to localStorage. The default is
// append (level 2): the gentle mode that supplements the ongoing thought
// without tearing down in-flight reasoning — friendlier for daily use than an
// interrupt. Interrupting is an explicit per-message choice. Once the user
// switches a worker's mode it sticks for that conversation partner.
const INPUT_MODES_KEY = 'niq.input-modes'
const DEFAULT_INPUT_MODE = 'append'

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
// On notched (full-bleed) iPhones the top bar must also clear the status-bar /
// notch inset (viewport-fit=cover), so both the bar height and the detail
// panel's top offset add env(safe-area-inset-top) — 0 everywhere else.
const mobileTopBarHeight = 'calc(' + MOBILE_TOP_BAR_HEIGHT + 'px + env(safe-area-inset-top, 0px))'
function MobileDetailPanel({ children }: { children: ReactNode }) {
  const { colors } = useTheme()
  return (
    <div style={{ position: 'fixed', top: mobileTopBarHeight, right: 0, bottom: 0, width: '100%', zIndex: 20, display: 'flex', background: colors.bg }}>
      {children}
    </div>
  )
}

// Merge incoming events into an existing list: dedupe by id and sort by
// timestamp (with an id tiebreak). Used for both live appends and history
// prepends so the timeline is independent of delivery order.
// When maxLen > 0, drops the oldest events so the retained list (and the
// DOM/render cost that follows it) stays bounded. maxLen is intentionally a
// per-call decision rather than a global: trimming happens ONLY on the live
// append path while the user is following the bottom, never on history
// prepends (which run when the user has scrolled up to read).
function mergeEvents(existing: EventPayload[], incoming: EventPayload[], maxLen = 0): EventPayload[] {
  const seen = new Set(existing.map((e) => e.id))
  const out = existing.slice()
  for (const e of incoming) {
    if (!seen.has(e.id)) {
      seen.add(e.id)
      out.push(e)
    }
  }
  out.sort((a, b) => a.timestamp - b.timestamp || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0))
  if (maxLen > 0 && out.length > maxLen) out.splice(0, out.length - maxLen)
  return out
}

// ── Talk worker filter in the URL ──
// The talk view's selected worker(s) live in `?talk=id1,id2`. Reading them on
// load means a refresh re-establishes the conversation, and writing them on
// every toggle keeps the selection durable without a full navigation.
const TALK_PARAM = 'talk'
function readTalkWorkersFromUrl(): Set<string> {
  const ids = new Set<string>()
  try {
    const v = new URLSearchParams(window.location.search).get(TALK_PARAM)
    if (v) for (const id of v.split(',')) if (id) ids.add(id)
  } catch { /* ignore malformed URL */ }
  return ids
}
function writeTalkWorkersToUrl(ids: Set<string>) {
  try {
    const q = new URLSearchParams(window.location.search)
    if (ids.size) q.set(TALK_PARAM, [...ids].sort().join(','))
    else q.delete(TALK_PARAM)
    const qs = q.toString()
    window.history.replaceState(null, '', window.location.pathname + (qs ? '?' + qs : ''))
  } catch { /* replaceState can throw in odd contexts */ }
}
// Local date-time stamp for the per-send timestamp reminder, e.g.
// "2026-09-17 15:30:05". Kept locale-independent so the reason worker can
// reliably read it regardless of the UI language.
function stampNow(): string {
  const d = new Date()
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// composeInput appends the staged attachments to the message text as HIW
// attachment envelope blocks (parsed worker-side and by the talk renderer), and
// prepends a `<system-reminder>` carrying the current time so the receiver
// knows when the message was sent.
function composeInput(text: string, attachments: StagedAttachment[]): string {
  const reminder = `<system-reminder>${stampNow()}</system-reminder>`
  const blocks = attachments.map(attachmentBlock)
  return [reminder, text.trim(), ...blocks].filter(Boolean).join('\n\n')
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
  const [workers, setWorkers] = useState<WorkerInfo[]>([])
  const [view, setView] = useState<ViewMode>('talk')
  const [context, setContext] = useState<ContextInfo>({ mode: 'project' })
  const [panel, setPanel] = useState<'projects' | 'templates' | 'providers' | null>(null)
  const [archived, setArchived] = useState<Set<string>>(new Set())
  const [input, setInput] = useState('')
  // Input mode is remembered per reason worker (the @ target the message is
  // aimed at), defaulting to append; see INPUT_MODES_KEY / DEFAULT_INPUT_MODE.
  const [inputModes, setInputModes] = useState<Record<string, string>>(() => {
    try { return JSON.parse(localStorage.getItem(INPUT_MODES_KEY) || '{}') } catch { return {} }
  })
  const [attachments, setAttachments] = useState<StagedAttachment[]>([])
  const [sending, setSending] = useState(false)
  // Bumped on each send; TalkView watches it to re-pin and scroll to the bottom
  // even if the user had scrolled up before sending.
  const [sendPulse, setSendPulse] = useState(0)
  const [mentionKey, setMentionKey] = useState(0)
  const [filterWorkers, setFilterWorkers] = useState<Set<string>>(new Set())
  const [selectedEventId, setSelectedEventId] = useState<string | null>(null)
  const [selectedWorkerId, setSelectedWorkerId] = useState<string | null>(null)
  // Worker shown in an in-place detail overlay (opened from the talk view by
  // right-clicking a badge), without navigating to the workers view.
  const [detailOverlayId, setDetailOverlayId] = useState<string | null>(null)
  const [detailWidth, setDetailWidth] = useState<number>(detailDefaultWidth)
  const detailDraggedRef = useRef(false)
  const [deliveries, setDeliveries] = useState<Record<string, string[]>>({})
  // Selected talk worker(s), restored from the URL (?talk=id1,id2) so a refresh
  // re-establishes the same conversation.
  const [talkWorkers, setTalkWorkers] = useState<Set<string>>(() => readTalkWorkersFromUrl())
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
  // The event currently shown in the detail panel. In the normal case it is
  // the row selected in the events list (selectedEventId); a one-shot
  // request_id lookup swaps it to the paired event (e.g. a tool call → its
  // request.* answer), which may not be in the loaded list. detailStack holds
  // the back-navigation history of that swap.
  const [detailEvt, setDetailEvt] = useState<EventPayload | null>(null)
  const [detailStack, setDetailStack] = useState<EventPayload[]>([])
  // Which side of the filtered workers' traffic to show: sent (they are the
  // source) / received (they are target or recipient). Both by default, which
  // matches the unfiltered-by-role behavior.
  const [filterRoles, setFilterRoles] = useState<Set<string>>(new Set(['sent', 'received']))

  const eventsRef = useRef<EventPayload[]>([])
  const seenRef = useRef<Set<string>>(new Set())
  const deliveriesRef = useRef<Record<string, string[]>>({})
  const watermarkRef = useRef('')
  const listRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)
  const sentinelRef = useRef<HTMLDivElement>(null)
  // Last scrollTop of the events list, to detect a manual up-scroll (the one
  // thing that turns the sticky follow switch off).
  const prevEventsScrollRef = useRef(0)
  // True briefly after a user gesture on the events scroller; only such a
  // gesture may turn the follow switch OFF (see TalkView for the rationale —
  // the follow-loop's programmatic re-pins must never look like a manual
  // scroll-up).
  const eventsManualRef = useRef(false)
  const eventsManualTimerRef = useRef(0)
  const markEventsManual = useCallback(() => {
    eventsManualRef.current = true
    window.clearTimeout(eventsManualTimerRef.current)
    eventsManualTimerRef.current = window.setTimeout(() => { eventsManualRef.current = false }, 400)
  }, [])
  // The currently-visible list's sticky follow-to-bottom switch (true = pinned
  // / following the live tail). Both the talk and events scrollers report
  // through setActiveFollowing; the trim gate only runs while this is true so
  // trimming never shifts a reader's scrolled-up viewport.
  const activeFollowingRef = useRef(true)
  // Trims are rare and big (only when the list crosses TRIM_HIGH), deferred
  // through scheduleTrim(0) so they land after the current render commit and
  // can't interleave with an in-flight streaming frame. Only while following.
  const trimTimerRef = useRef(0)
  // Trim the live list down to TRIM_LOW (newest window). Only runs while
  // following, so a scrolled-up reader is never shifted. The list is server-
  // scoped to the selected worker(s), so trimming can't drop a watched
  // conversation the way the old global feed did.
  const trimToLow = useCallback(() => {
    if (!activeFollowingRef.current || eventsRef.current.length <= TRIM_LOW) return
    const trimmed = mergeEvents(eventsRef.current, [], TRIM_LOW)
    eventsRef.current = trimmed
    seenRef.current = new Set(trimmed.map((e) => e.id))
    setEvents(trimmed)
  }, [])
  const scheduleTrim = useCallback((delay: number) => {
    window.clearTimeout(trimTimerRef.current)
    trimTimerRef.current = window.setTimeout(trimToLow, delay)
  }, [trimToLow])
  const setActiveFollowing = useCallback((v: boolean) => {
    const was = activeFollowingRef.current
    activeFollowingRef.current = v
    if (!v) {
      // Reading up: cancel any queued trim so we never collapse history while
      // the reader is passing back through it.
      window.clearTimeout(trimTimerRef.current)
      return
    }
    // Re-engaging follow (user landed at the bottom): reclaim excess right away.
    if (!was) scheduleTrim(0)
  }, [scheduleTrim])
  // Switching the visible view mounts a fresh scroller, which starts pinned to
  // the bottom until it reports its first scroll.
  useEffect(() => { activeFollowingRef.current = true }, [view])
  // Mirrors autoScrollRef for rendering: the scroll-to-bottom button shows
  // while the list isn't pinned to the bottom.
  const [eventsAtBottom, setEventsAtBottom] = useState(true)

  // Trim on input focus: as soon as the user is about to type, reclaim the list
  // down to TRIM_LOW so typing never contends with a huge DOM.
  const handleInputFocus = useCallback(() => {
    if (activeFollowingRef.current && eventsRef.current.length > TRIM_LOW) scheduleTrim(0)
  }, [scheduleTrim])

  // ── Mode: control (no project attached) vs project. In control mode only the
  // projects surface is usable; talk/events/workers need an attached project, so
  // they are hidden and their SSE + polling are disabled. The control plane
  // serves everything on its own origin — a project WebUI is reached through it
  // at /p/<id>/ — so project management APIs live at the root (CONTROL) while a
  // project's own APIs carry the /p/<id> base (see services/api.ts).
  const mode = context.mode
  const projectBase = getApiBase()
  const projectName = context.project

  // Page title: append the attached project name when there is one.
  useEffect(() => {
    document.title = projectName ? `niq · ${projectName}` : 'niq'
  }, [projectName])

  // Is the attached project actually running? Polled from the control plane
  // (independent of this page's own backend, which is exactly what dies when
  // the project stops). undefined = unknown (first poll pending or control
  // unreachable) — the banner stays hidden so a healthy page never flashes it.
  const [projRunning, setProjRunning] = useState<boolean | undefined>(undefined)
  const [startingProj, setStartingProj] = useState(false)
  const [startProjErr, setStartProjErr] = useState('')
  usePolling<ProjectInfo[]>(CONTROL + '/api/projects', 5000, (list) => {
    if (mode !== 'project' || !projectName) return
    const p = list.find((x: ProjectInfo) => x.id === projectName)
    setProjRunning(p ? !!p.running : false)
  }, mode === 'project' && projectName !== '')

  // Start the current project from a stale page: the start call blocks until
  // the project's WebUI is listening, so a successful return only needs the
  // page reloaded — this page is already the project's own URL (/p/<id>/ when
  // it is reached through the control plane).
  const startCurrentProject = async () => {
    if (!projectName || startingProj) return
    setStartingProj(true)
    setStartProjErr('')
    try {
      await startProject(projectName)
      window.location.reload()
    } catch (e) {
      setStartProjErr((e as Error)?.message || 'start failed')
    }
    setStartingProj(false)
  }

  // Sidebar footer project menu: restart / stop the attached project from the
  // control plane, same semantics as the projects page. Restart just reloads
  // the page in place once the call returns; no port-hop is needed.
  // Stop intentionally leaves the page stale — the stopped banner offers the
  // way back. Errors surface inside the menu.
  const [projBusy, setProjBusy] = useState<'' | 'stop' | 'restart'>('')
  const [projActionErr, setProjActionErr] = useState('')
  const restartCurrentProject = async () => {
    if (!projectName || projBusy) return
    setProjBusy('restart')
    setProjActionErr('')
    try {
      await restartProject(projectName)
      window.location.reload()
    } catch (e) {
      setProjActionErr((e as Error)?.message || 'restart failed')
      setProjBusy('')
    }
  }
  const stopCurrentProject = async () => {
    if (!projectName || projBusy) return
    setProjBusy('stop')
    setProjActionErr('')
    try {
      await stopProject(projectName)
    } catch (e) {
      setProjActionErr((e as Error)?.message || 'stop failed')
    }
    setProjBusy('')
  }

  useEffect(() => {
    // Ask /api/context which mode this page is in: the control plane's own page
    // answers "control", a project page (served under /p/<id>/) answers
    // "project" with its id.
    fetchContext().then(setContext).catch(() => {})
  }, [])

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
  // The SSE is a *project-level* subscription to the event stream. It
  // reconnects (rebuilding the timeline) whenever the stream's filter actually
  // changes: the events view's worker/trace filter, or the talk view's selected
  // conversation worker(s). Selecting a talk worker therefore tears down and
  // re-establishes the stream scoped to that worker server-side — the events
  // array holds only that conversation, so the newest-N trim can never drop it
  // and the earlier client-side re-scope fetch is unnecessary.
  const streamKey = view === 'events'
    ? 'events-' + [...filterWorkers].sort().join(',') + '-' + [...filterRoles].sort().join(',') + '-' + traceFilter
    : view === 'talk'
      ? 'talk-' + [...talkWorkers].sort().join(',')
      : 'all'
  // Mirrors streamKey for async callbacks (the history fetch) to detect that
  // their stream was torn down while the request was in flight.
  const streamKeyRef = useRef(streamKey)
  streamKeyRef.current = streamKey

  // The talk view's pagination scope: the reason workers it's currently
  // watching. Passed to the events/before API (worker_id OR target OR
  // recipient, the same envelope semantics as TalkView's relevantEvents) so
  // older talk events page back directly over the conversation. Without this,
  // entering the talk view (or selecting a worker) walks whole mostly
  // system-noise history pages, firing a burst of requests and delaying first
  // paint until the walk happens to reach a reason event.
  const talkScope = useMemo(() => {
    if (talkWorkers.size > 0) return [...talkWorkers]
    const reason: string[] = []
    for (const w of workers) if (isTalkPartnerType(w.type)) reason.push(w.id)
    return reason
  }, [talkWorkers, workers])

  useEffect(() => {
    // No project → no event stream.
    if (mode !== 'project') return
    // The SSE is a project-level subscription; its filter follows the active
    // view. The events view scopes by its worker/trace filter; the talk view
    // scopes to the selected conversation worker(s) (worker_id OR target OR
    // recipient — the same envelope semantics as TalkView's relevantEvents and
    // the backend's workerMatchesAny), so the stream only ever ships that
    // conversation. An empty talk selection means the unfiltered stream.
    const params = new URLSearchParams()
    if (view === 'events') {
      for (const id of filterWorkers) params.append('worker', id)
      for (const role of filterRoles) params.append('role', role)
      if (traceFilter) params.set('trace', traceFilter)
    } else if (view === 'talk') {
      for (const id of talkWorkers) params.append('worker', id)
    }
    const url = projectBase + `/api/stream?${params}`

    // Any streamKey change clears the timeline immediately — the incoming
    // stream has a different scope, so no stale rows may flash before history
    // arrives. This covers entering/leaving the events view and its filter
    // changes alike: switching back to talk from a filtered events stream
    // must rebuild the full timeline, not keep the filtered leftovers
    // (otherwise the tail shows stale rows and whole stretches in between
    // are missing).
    setEvents([])
    eventsRef.current = []
    seenRef.current.clear()
    // A rebuilt timeline remounts its scroller pinned to the bottom.
    activeFollowingRef.current = true
    setDeliveries({})
    deliveriesRef.current = {}
    setSelectedEventId(null)
    setDetailEvt(null)
    setDetailStack([])

    // Page backwards from the watermark to fill history. Issued once the stream
    // advertises its watermark. History is merged into the current timeline
    // (dedup by id, sorted) rather than replacing it: the live stream may have
    // already delivered events — including the watermark event itself — and a
    // wipe would drop them. The merged result is the same clean, correctly-
    // ordered timeline a rebuild would produce.
    const myKey = streamKey
    const loadInitialHistory = async (watermark: string) => {
      if (!watermark) return
      noMoreRef.current = false
      const limit = HISTORY_PAGE
      const workers = view === 'events' ? [...filterWorkers] : view === 'talk' ? talkScope : []
      const roles = view === 'events' ? [...filterRoles] : []
      const trace = view === 'events' ? traceFilter : ''
      try {
        // Merge history into whatever the live stream has already delivered
        // (the watermark event itself arrives this way) instead of wiping:
        // events consumed between connect and this response are newer than
        // the history page and would be lost to a wipe. mergeEvents dedupes
        // by id and sorts, so the result is the clean timeline a rebuild
        // would produce. The streamKey guard drops responses from a torn-
        // down stream, so a slow fetch can't pollute the successor timeline.
        const older = (await loadEventsBefore(watermark, limit, workers, trace, roles)) as EventPayload[]
        if (streamKeyRef.current !== myKey) return
        const filtered = older.filter((e) => e.type !== 'event.delivered')
        const merged = mergeEvents(eventsRef.current, filtered)
        eventsRef.current = merged
        setEvents(merged)
      } catch {}
    }

    const es = new EventSource(url)
    // The server advertises the subscription watermark as a control event before
    // any data; we use it to kick off backwards pagination for history.
    const onWatermark = (e: MessageEvent) => { watermarkRef.current = e.data as string; loadInitialHistory(e.data as string) }
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
      // Steady live-stream events arrive in order, so the common case is a cheap
      // sorted append: build one new array (for React to see a new reference)
      // but skip the full-array sort and the dedupe-set rebuild. Only a rare
      // genuinely out-of-order delivery falls back to mergeEvents, which sorts
      // to keep the timeline ordered. This is the difference between a per-event
      // O(n·log n) sort and a linear copy — with thousands of rows that was the
      // dominant dom/script cost, which is why trims had to kick in so early.
      const prev = eventsRef.current
      const lastEvt = prev[prev.length - 1]
      const inOrder = !!lastEvt && (
        evt.timestamp > lastEvt.timestamp ||
        (evt.timestamp === lastEvt.timestamp && evt.id > lastEvt.id)
      )
      let next: EventPayload[]
      if (inOrder) {
        next = prev.concat([evt])
        eventsRef.current = next
        seenRef.current.add(evt.id)
        setEvents(next)
      } else {
        next = mergeEvents(prev, [evt])
        eventsRef.current = next
        setEvents(next)
        // Rebase the dedupe set to the retained ids (only needs doing on the
        // rare out-of-order path; the in-order path is kept identical by add).
        seenRef.current = new Set(next.map((e) => e.id))
      }
      if (activeFollowingRef.current && next.length >= TRIM_HIGH) {
        scheduleTrim(0)
      }
    }
    return () => {
      es.close()
      window.clearTimeout(trimTimerRef.current)
    }
    // Re-run whenever the filter changes (events view), or the selected talk
    // conversation changes — both are captured in streamKey. `view` itself is
    // intentionally not a dep; it is already encoded by streamKey.
  }, [streamKey, mode, projectBase])

  // ── Polling (only meaningful when a project is attached). The URL carries the
  // project base so a project page reached through /p/<id>/ hits that project's
  // own API rather than the control plane's.
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

  // ── Approvals: the HIW tracks boundary-expansion approval requests; the
  // app polls them for the nav badge and passes them to the approvals view and
  // the talk view's inline quick-approve.
  const [approvals, setApprovals] = useState<ApprovalEntry[]>([])
  useEffect(() => {
    if (mode !== 'project') { setApprovals([]); return }
    let alive = true
    const load = () => fetchApprovals()
      .then(r => { if (alive) setApprovals(r.approvals ?? []) })
      .catch(() => { /* next poll retries */ })
    load()
    const t = setInterval(load, 5000)
    return () => { alive = false; clearInterval(t) }
  }, [mode, projectBase])

  const pendingApprovalCount = approvals.filter(a => !a.decision).length

  // decide resolves one approval through HIW and refreshes immediately so the
  // views reflect the decision without waiting for the next poll.
  const handleDecide = useCallback(async (id: string, approved: boolean, note = '') => {
    try {
      await decideApproval(id, approved, note)
    } catch { /* surfaced via the entry's state on next poll */ }
    fetchApprovals().then(r => setApprovals(r.approvals ?? [])).catch(() => {})
  }, [])

  // ── Callbacks ──
  // Which reason worker the input is currently aimed at — drives the per-worker
  // input mode. The @ target wins, else the first selected talk worker.
  const activeWorker = useMemo(() => {
    const reasons = workers.filter(w => isTalkPartnerType(w.type))
    if (mentionTarget && reasons.some(r => r.id === mentionTarget)) return mentionTarget
    const sel = [...talkWorkers].filter(id => reasons.some(r => r.id === id))
    return sel.length ? sel[0] : ''
  }, [mentionTarget, talkWorkers, workers])
  const currentInputMode = inputModes[activeWorker] || DEFAULT_INPUT_MODE

  // Persist a per-worker mode change under the active worker.
  const handleInputModeChange = useCallback((m: string) => {
    setInputModes(prev => {
      const next = { ...prev, [activeWorker]: m }
      try { localStorage.setItem(INPUT_MODES_KEY, JSON.stringify(next)) } catch {}
      return next
    })
  }, [activeWorker])

  const sendMessage = useCallback(() => {
    if (!input.trim() || sending) return
    setSending(true)
    // Returning to the live bottom: bump the signal TalkView listens for.
    setSendPulse(p => p + 1)
    // Parse @mention for targeting a specific worker.
    let msgTarget = ''
    let msgText = input
    const mentionMatch = input.match(/^@(\S+)\s+(.*)$/s)
    if (mentionMatch) {
      const mentioned = mentionMatch[1]
      const reasonWorkers = workers.filter(w => isTalkPartnerType(w.type))
      if (reasonWorkers.some(r => r.id === mentioned)) {
        msgTarget = mentioned
        msgText = mentionMatch[2]
        setMentionTarget(mentioned) // persist the @ target for the next message
      }
    }
    if (!msgTarget) {
      // No @mention: reuse the persisted target if still valid; otherwise the
      // first selected reason worker, or broadcast.
      const reasonWorkers = workers.filter(w => isTalkPartnerType(w.type))
      if (mentionTarget && reasonWorkers.some(r => r.id === mentionTarget)) {
        msgTarget = mentionTarget
      } else {
        const selectedReasons = [...talkWorkers].filter(id => reasonWorkers.some(r => r.id === id))
        msgTarget = selectedReasons.length > 0 ? selectedReasons[0] : ''
      }
    }
    sendInput(composeInput(msgText, attachments), msgTarget, currentInputMode).then(() => {
      setInput('')
      setAttachments([])
      setSending(false)
    }).catch(() => {
      setSending(false)
    })
  }, [input, view, talkWorkers, currentInputMode, sending, workers, mentionTarget, activeWorker, attachments])

  const handleAbort = useCallback(() => {
    const reasonWorkers = workers.filter(w => isTalkPartnerType(w.type))
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

  // Sidebar worker selector (talk view): multi-select toggle. On each selection
  // change we keep the talk (mention) target aligned with the selection boundary
  // cases the toggle can produce:
  //   * single selection      -> target = that single worker
  //   * empty selection       -> clear the target (no worker is watched; a stale
  //                              target would silently keep routing sends/aborts)
  //   * multi-selection       -> leave a manual target alone (it may point at a
  //                              worker outside the watched set by design)
  // The critical detail: when a deselect changes a pair {A,B} into the single B,
  // the toggled id is the *removed* A — so the target must be the worker actually
  // left in the set, i.e. next's sole element, not the toggled id.
  const toggleWorker = useCallback((id: string) => {
    const next = new Set(talkWorkers)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    setTalkWorkers(next)
    writeTalkWorkersToUrl(next)
    if (next.size === 1) {
      setMentionTarget([...next][0])
    } else if (next.size === 0) {
      setMentionTarget('')
    }
  }, [talkWorkers])

  // Talk-avatar menu helpers that keep you in the talk view: "add filter"
  // ensures the worker is in the watched conversation set, "show only this
  // worker" narrows it to just that worker. Calling setTalkWorkers re-scopes the
  // talk stream server-side (streamKey), so picking them visibly changes which
  // conversation the talk timeline shows.
  const addFilterWorker = useCallback((id: string) => {
    if (talkWorkers.has(id)) return
    const next = new Set(talkWorkers)
    next.add(id)
    setTalkWorkers(next)
    writeTalkWorkersToUrl(next)
    if (next.size === 1) setMentionTarget(id)
  }, [talkWorkers])
  const filterOnlyWorker = useCallback((id: string) => {
    if (talkWorkers.size === 1 && talkWorkers.has(id)) return
    const next = new Set([id])
    setTalkWorkers(next)
    writeTalkWorkersToUrl(next)
    setMentionTarget(id)
  }, [talkWorkers])

  const selectEvent = useCallback((id: string) => {
    setSelectedEventId(prev => {
      const next = prev === id ? null : id
      return next
    })
    // Sync the detail panel to the tapped row and reset any request-pair
    // back-navigation stack.
    const evt = events.find(e => e.id === id)
    if (evt) {
      setDetailEvt(evt)
      setDetailStack([])
    }
  }, [events])

  const selectWorker = useCallback((id: string) => {
    setSelectedWorkerId(prev => prev === id ? null : id)
  }, [])

  // Open a worker's detail page directly (e.g. right-click on a talk badge):
  // select it and switch to the workers view, which renders the detail panel.
  // Open a worker's detail page as an in-place overlay (e.g. right-click on a
  // talk badge), without navigating away from the current view.
  const openWorkerDetail = useCallback((id: string) => {
    setDetailOverlayId(id)
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

  // Mentioning a worker from a talk badge prefixes the input and pins the
  // target. Kept stable (useCallback) so the memoized talk rows don't re-render
  // on every event.
  const handleMention = useCallback((id: string) => {
    setInput(prev => prev + '@' + id + ' ')
    setMentionTarget(id)
    setMentionKey(k => k + 1)
  }, [])

  const clearTraceFilter = useCallback(() => {
    setTraceFilter('')
  }, [])

  // One-shot request lookup: given the event shown in the detail panel (which
  // carries a request_id), fetch every persisted event sharing that id and swap
  // the panel to its pair — from an invocation to its request.* answer, or from
  // a request.* answer back to the invocation that started it. This is a
  // standalone query, fully decoupled from the live event stream.
  const handleFindRequestPair = useCallback(async () => {
    if (!detailEvt?.request_id) return
    const rid = detailEvt.request_id
    const isReply = detailEvt.type === 'request.completed' || detailEvt.type === 'request.failed' || detailEvt.type === 'request.rejected'
    let paired: EventPayload | undefined
    try {
      const all = (await fetchEventsByRequest(rid)) as EventPayload[]
      if (isReply) {
        // We're on the answer: jump to the invocation that started it (a
        // domain-typed event, i.e. not a request.* reply).
        paired = all.find(e => !e.type.startsWith('request.'))
      } else {
        // We're on the invocation: prefer the terminal answer over the
        // intermediate request.progressed lifecycle events.
        paired = all.find(e => e.type === 'request.completed' || e.type === 'request.failed' || e.type === 'request.rejected')
      }
    } catch { /* query failed; leave the panel unchanged */ }
    if (!paired || paired.id === detailEvt.id) return
    setDetailStack(prev => [...prev, detailEvt])
    setSelectedEventId(paired.id)
    setDetailEvt(paired)
  }, [detailEvt])

  // Back out of a request-pair jump to the previously shown event.
  const handleDetailBack = useCallback(() => {
    setDetailStack(prev => {
      if (prev.length === 0) return prev
      const last = prev[prev.length - 1]
      setDetailEvt(last)
      setSelectedEventId(last.id)
      return prev.slice(0, -1)
    })
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

  // Toggle one traffic role (sent/received) in the events filter. Both roles
  // checked is the default and matches the unfiltered-by-role behavior.
  const toggleFilterRole = useCallback((role: string) => {
    setFilterRoles(prev => {
      const next = new Set(prev)
      if (next.has(role)) next.delete(role)
      else next.add(role)
      return next
    })
  }, [])

  // ── Load more older events ──
  // Guards that keep the top sentinel from re-triggering in a loop: an
  // in-flight lock (the observer fires again while a fetch is pending) and an
  // exhausted flag (the store returned fewer than a full page — nothing older
  // matches the current filter, so stop asking).
  const loadingMoreRef = useRef(false)
  const noMoreRef = useRef(false)
  // Viewport anchor for the prepend: captured before setEvents, applied by the
  // layout effect below so the visible frame doesn't jump and the sentinel
  // moves out of view instead of staying pinned at the top.
  const pendingAnchorRef = useRef<{ top: number; height: number } | null>(null)

  // LOAD_EARLY_PX: how far below the top the events list starts prefetching
  // older events, so the page lands while there is still room to scroll.
  const LOAD_EARLY_PX = 480
  const loadMore = useCallback(async () => {
    if (loadingMoreRef.current || noMoreRef.current) return
    if (events.length === 0) return
    loadingMoreRef.current = true
    try {
      const anchorEl = listRef.current
      const anchor = anchorEl ? { top: anchorEl.scrollTop, height: anchorEl.scrollHeight } : null
      const workers = view === 'events' ? [...filterWorkers] : view === 'talk' ? talkScope : []
      const roles = view === 'events' ? [...filterRoles] : []
      const trace = view === 'events' ? traceFilter : ''
      const older = (await loadEventsBefore(events[0].id, HISTORY_PAGE, workers, trace, roles)) as EventPayload[]
      // Fewer than a full page means the store has nothing older that matches.
      if (older.length < HISTORY_PAGE) noMoreRef.current = true
      const filtered = older.filter((e) => e.type !== 'event.delivered')
      if (filtered.length === 0) return
      const merged = mergeEvents(eventsRef.current, filtered)
      eventsRef.current = merged
      setEvents(merged)
      pendingAnchorRef.current = anchor
    } catch {} finally {
      loadingMoreRef.current = false
    }
  }, [events, view, filterWorkers, filterRoles, traceFilter, talkScope])

  // ── Events list auto-scroll ──
  // Layout effect (before paint) so the initial load lands directly at the
  // bottom instead of painting the top first. When pinned to the bottom we
  // keep the bottom edge anchored with an INSTANT pin (never smooth): the
  // backend pushes live updates periodically and a smooth re-scroll on each
  // burst reads as a visible stutter. Newest rows just push older ones up.
  useLayoutEffect(() => {
    if (autoScrollRef.current && listRef.current) {
      listRef.current.scrollTop = listRef.current.scrollHeight
    }
  }, [events])

  // Anchor the viewport across a load-more prepend: without this the content
  // grows above the viewport, the view appears to jump, and the top sentinel
  // stays visible — re-triggering the loader in a loop.
  useLayoutEffect(() => {
    const a = pendingAnchorRef.current
    pendingAnchorRef.current = null
    const el = listRef.current
    if (!a || !el) return
    const grew = el.scrollHeight - a.height
    if (grew > 0) el.scrollTop = a.top + grew
  }, [events])

  // A new filter scope may have older events again — clear the exhausted flag
  // set by a previous history walk. talkWorkers is included because the talk
  // view pages by its selection scope, so switching the selected worker must
  // re-open its own history.
  useEffect(() => {
    noMoreRef.current = false
  }, [view, filterWorkers, filterRoles, traceFilter, talkScope])

  // Auto-load more events when scrolling to top.
  useEffect(() => {
    if (view !== 'events' || events.length === 0) return
    const el = sentinelRef.current
    if (!el) return
    const observer = new IntersectionObserver((entries) => {
      if (!entries[0].isIntersecting) return
      // Same runaway guard as the talk sentinel: only auto-load when the list
      // actually overflows, so a short/empty (fully-filtered) timeline can't
      // keep the top sentinel in view and page forever.
      const list = listRef.current
      if (list && list.scrollHeight <= list.clientHeight + 1) return
      loadMore()
    }, { rootMargin: '600px 0px' })
    observer.observe(el)
    return () => observer.disconnect()
  }, [view, events.length, loadMore])

  // Keep a current copy of loadMore for the stable handleScroll callback so
  // that it can trigger an early prefetch without going stale.
  const loadMoreRef = useRef(loadMore)
  loadMoreRef.current = loadMore

  const handleScroll = useCallback(() => {
    const el = listRef.current
    if (!el) return
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight
    const prev = prevEventsScrollRef.current
    // Sticky follow switch (mirrors TalkView): off only on an explicit up-
    // scroll, on again once the user gets back down to the bottom. Content
    // growth never flips it, so a message pushing the bottom past the old
    // threshold can't accidentally drop follow.
    if (autoScrollRef.current && eventsManualRef.current && el.scrollTop < prev) {
      autoScrollRef.current = false
    } else if (!autoScrollRef.current && el.scrollTop > prev && dist < 50) {
      autoScrollRef.current = true
    }
    prevEventsScrollRef.current = el.scrollTop
    // Drives the scroll-to-bottom button; React bails out when unchanged.
    setEventsAtBottom(autoScrollRef.current)
    setActiveFollowing(autoScrollRef.current)
    // Early prefetch: approach the top -> start loading older events while
    // there is still scroll room, so the prepend lands before reaching the
    // oldest row. scrollTop is the remaining distance upward; the prepend's
    // anchor correction pushes it past LOAD_EARLY_PX so this can't loop, and a
    // non-overflowing (short/empty) timeline is left to the sentinel/backfill.
    if (el.scrollTop < LOAD_EARLY_PX && el.scrollHeight > el.clientHeight + 1) {
      loadMoreRef.current?.()
    }
  }, [setActiveFollowing])

  const workerTypes = useMemo(() => {
    const map: Record<string, string> = {}
    for (const w of workers) {
      map[w.id] = w.type
    }
    return map
  }, [workers])

  // Latest worker.ready "watch" per worker: every announced capability
  // (event type + parameter schema) the human UI can drive. Largely derived by
  // taking the most recent worker.ready broadcast for each worker id.
  const workerWatch = useMemo(() => {
    const map: Record<string, WatchEntry[]> = {}
    for (const evt of events) {
      if (evt.type !== 'worker.ready') continue
      const wid = evt.worker_id || (evt.payload?.worker_id as string | undefined)
      if (!wid) continue
      const raw = evt.payload?.watch
      if (!Array.isArray(raw)) continue
      const entries = raw
        .map((e: any): WatchEntry | null => {
          if (!e || typeof e.event !== 'string') return null
          return { event: e.event, desc: e.desc, parameters: e.parameters }
        })
        .filter((e): e is WatchEntry => e !== null)
      map[wid] = entries
    }
    return map
  }, [events])

  // When a worker detail opens and we don't yet know its capabilities
  // (worker.ready is streamed live but not persisted, so a fresh detail has no
  // watch history to read), ask that worker on demand with a *directed*
  // worker.discover. It re-announces its worker.ready, which rides the SSE
  // back and populates workerWatch[id] — one worker, on demand, no broadcast.
  const discoverAskedRef = useRef<Set<string>>(new Set())
  useEffect(() => {
    const id = detailOverlayId || selectedWorkerId
    if (!id) {
      discoverAskedRef.current.clear()
      return
    }
    if (workerWatch[id]?.length) return // already know its capabilities
    if (discoverAskedRef.current.has(id)) return
    discoverAskedRef.current.add(id)
    const w = workers.find(x => x.id === id)
    if (w && w.online === false) return // offline: don't spam
    sendWorkerEvent(id, 'worker.discover', {}).catch(() => {})
  }, [detailOverlayId, selectedWorkerId, workerWatch, workers])

  // Worker currently shown in the right-hand detail panel (workers view only).
  const selectedWorker = view === 'workers' ? workers.find(w => w.id === selectedWorkerId) : undefined

  // ── Render ──
  return (
    <div data-theme={dark ? 'dark' : 'light'} className="app-shell" style={{ display: 'flex', color: colors.text, background: colors.bg }}>
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
        projRunning={projRunning}
        projBusy={projBusy}
        projStarting={startingProj}
        projActionErr={projActionErr}
        onProjectStart={startCurrentProject}
        onProjectStop={stopCurrentProject}
        onProjectRestart={restartCurrentProject}
        panel={panel}
        onSelectPanel={setPanel}
        archived={archived}
        pendingApprovals={pendingApprovalCount}
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
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, height: mobileTopBarHeight, boxSizing: 'border-box', padding: '0 16px', paddingTop: 'env(safe-area-inset-top, 0px)', borderBottom: '1px solid ' + colors.border, flexShrink: 0, background: colors.bg, zIndex: 10 }}>
            <button
              onClick={() => setSidebarOpen(true)}
              title={t('app.menu')}
              style={{ background: 'none', border: '1px solid ' + colors.border, borderRadius: 2, padding: '5px 8px', cursor: 'pointer', display: 'inline-flex', alignItems: 'center', justifyContent: 'center' }}
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
                : panel === 'providers' ? t('sidebar.providers')
                : panel === 'projects' ? t('sidebar.projects')
                : mode !== 'project' ? t('sidebar.projects')
                : view === 'talk' ? t('nav.talk')
                : view === 'events' ? t('nav.events')
                : view === 'approvals' ? t('nav.approvals')
                : view === 'programs' ? t('sidebar.programs')
                : t('nav.workers')}
            </strong>
          </div>
        )}
        {mode === 'project' && projectName && projRunning === false && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 24px', borderBottom: '1px solid ' + colors.border, background: colors.bgLight, flexShrink: 0 }}>
            <span style={{ color: colors.toolFailed, fontSize: fontSizes.sm }}>●</span>
            <span style={{ flex: 1, color: colors.text, fontSize: fontSizes.sm }}>
              {t('project.stoppedBanner')}
            </span>
            {startProjErr && (
              <span title={startProjErr} style={{ color: colors.toolFailed, fontSize: fontSizes.sm, maxWidth: '40%', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{startProjErr}</span>
            )}
            <span
              onClick={startCurrentProject}
              className="btn-hover"
              style={{ cursor: startingProj ? 'default' : 'pointer', opacity: startingProj ? 0.6 : 1, display: 'inline-flex', alignItems: 'center', gap: 6, border: '1px solid ' + colors.accent, borderRadius: 2, padding: '3px 12px', color: colors.accent, fontSize: fontSizes.sm, userSelect: 'none' }}
            >
              {startingProj && <span className="niq-spinner" style={{ width: 12, height: 12, borderWidth: 2, borderColor: colors.accent, borderTopColor: 'transparent' }} />}
              {startingProj ? t('projects.starting') : t('projects.start')}
            </span>
          </div>
        )}
        {mode !== 'project' ? (
          panel === 'templates' ? <TemplatesView isMobile={isMobile} /> : panel === 'providers' ? <ProvidersView /> : <ProjectsView onGoToProviders={() => setPanel('providers')} />
        ) : panel === 'templates' ? (
          <TemplatesView isMobile={isMobile} />
        ) : panel === 'providers' ? (
          <ProvidersView />
        ) : panel === 'projects' ? (
          <ProjectsView />
        ) : view === 'programs' ? (
          <div key="programs" className="fade-in" style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
            <ProgramsView project={projectName} isMobile={isMobile} />
          </div>
        // Re-key the talk wrapper on the stream scope so switching the selected
        // conversation remounts with the existing fade-in instead of a hard cut
        // to the new (possibly empty-while-loading) list. Mirrors the events
        // view, which re-keys the same way on its filter stream.
        ) : view === 'talk' ? (
          <div key={streamKey} className="fade-in" style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
            <TalkView
              events={events}
              talkWorkers={talkWorkers}
              onTraceClick={handleTraceClick}
              onLoadMore={loadMore}
              onMention={handleMention}
              onOpenDetail={openWorkerDetail}
              onAddFilter={addFilterWorker}
              onFocusWorker={filterOnlyWorker}
              deliveries={deliveries}
              workerTypes={workerTypes}
              thinkingExpanded={viewSettings.thinkingExpanded}
              compactMode={viewSettings.compactMode}
              streamingMode={viewSettings.streamingMode}
              responseOnly={viewSettings.responseOnly}
              isMobile={isMobile}
              onDecide={handleDecide}
              scrollToBottomSignal={sendPulse}
              onFollowChange={setActiveFollowing}
            />

            <TalkInput
              talkPartner={''}
              input={input}
              inputMode={currentInputMode}
              onInputChange={setInput}
              onSend={sendMessage}
              onAbort={handleAbort}
              onModeChange={handleInputModeChange}
              workers={workers}
              archived={archived}
              mentionKey={mentionKey}
              mentionTarget={mentionTarget}
              onClearMentionTarget={() => setMentionTarget('')}
              onSelectTarget={(id) => setMentionTarget(id)}
              isMobile={isMobile}
              attachments={attachments}
              onAttachmentsChange={setAttachments}
              onFocus={handleInputFocus}
            />
          </div>
        ) : view === 'approvals' ? (
          <div key="approvals" className="fade-in" style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' }}>
            <ApprovalsView approvals={approvals} onDecide={handleDecide} isMobile={isMobile} />
          </div>
        ) : view === 'workers' ? (
          <div key="workers" className="fade-in" style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
            <WorkersView
              workers={workers}
              archived={archived}
              selectedId={selectedWorkerId}
              onSelect={selectWorker}
              onOpenEvents={handleSelectWorker}
              isMobile={isMobile}
              onRefresh={refreshWorkers}
                busPort={context.bus_port}
            />
            {selectedWorker && (
              isMobile ? (
                <MobileDetailPanel>
                  <WorkerDetail
                    worker={selectedWorker}
                    allWorkers={workers}
                    watch={workerWatch[selectedWorker.id] ?? []}
                    onClose={() => setSelectedWorkerId(null)}
                  						archived={archived}
                  						onToggleArchived={toggleArchived}
                  						onDeleted={(id) => { setSelectedWorkerId(null); refreshWorkers() }}
                  						onRefresh={refreshWorkers}
                busPort={context.bus_port}
                  					/>
                  				</MobileDetailPanel>
                  			  ) : (
				                <ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
				                  <WorkerDetail
				                    worker={selectedWorker}
				                    allWorkers={workers}
				                    watch={workerWatch[selectedWorker.id] ?? []}
				                    onClose={() => setSelectedWorkerId(null)}
                  						archived={archived}
                  						onToggleArchived={toggleArchived}
                  						onDeleted={(id) => { setSelectedWorkerId(null); refreshWorkers() }}
                  						onRefresh={refreshWorkers}
                busPort={context.bus_port}
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
              <ViewHeader
                title={t('nav.events')}
                right={
                <span style={{ fontSize: fontSizes.sm, color: colors.textMuted, display: 'flex', alignItems: 'center', gap: 8 }}>
                  {filterWorkers.size > 0 && (
                    <>
                      {t('events.filtering')} <strong style={{ color: colors.textDim }}>[{[...filterWorkers].join(', ')}]</strong>
                      {/* Traffic direction: which side of the filtered
                          workers' events to show. Both checked = default. */}
                      {(['sent', 'received'] as const).map(role => (
                        <label key={role} style={{ display: 'inline-flex', alignItems: 'center', gap: 4, marginLeft: 12, cursor: 'pointer', color: colors.textDim, fontSize: fontSizes.sm }}>
                          <input type="checkbox" checked={filterRoles.has(role)} onChange={() => toggleFilterRole(role)} style={{ margin: 0, accentColor: colors.accent }} />
                          {t(role === 'sent' ? 'events.role.sent' : 'events.role.received')}
                        </label>
                      ))}
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
              }
            />
            )}

            <div key="events" className="fade-in" style={{ flex: 1, position: 'relative', display: 'flex', overflow: 'hidden' }}>
              <div key={streamKey} ref={listRef} onScroll={handleScroll} onWheel={markEventsManual} onPointerDown={markEventsManual} onTouchStart={markEventsManual} className="fade-in" style={{ flex: 1, minWidth: 0, overflow: 'auto', fontSize: fontSizes.md, padding: '0 24px 16px 24px', overflowAnchor: 'none' }}>
                {events.length > 0 && <div ref={sentinelRef} style={{ height: 1 }} />}
                {/* min-width lets the wide fixed columns scroll horizontally on
                    narrow (phone) viewports instead of collapsing. */}
                <table style={{ width: '100%', minWidth: 680, borderCollapse: 'separate', borderSpacing: 0, tableLayout: 'fixed', fontSize: fontSizes.md }}>
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
              {/* Floating jump-to-bottom: shown while the list isn't pinned to
                  the bottom. Clicking re-pins so live updates follow again. */}
              {!eventsAtBottom && (
                <button
                  onClick={() => {
                    autoScrollRef.current = true
                    setEventsAtBottom(true)
                    setActiveFollowing(true)
                    listRef.current?.scrollTo({ top: listRef.current.scrollHeight, behavior: 'smooth' })
                  }}
                  title={t('app.scrollToBottom')}
                  style={{
                    position: 'absolute', bottom: 16, left: '50%', transform: 'translateX(-50%)',
                    width: 36, height: 36, borderRadius: '50%', padding: 0,
                    border: '1px solid ' + colors.border, background: colors.bg, color: colors.textDim,
                    cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center',
                    boxShadow: '0 2px 8px rgba(0,0,0,0.25)', zIndex: 5,
                  }}
                >
                  {/* Drawn arrow — glyph baselines sit off-center in the mono font. */}
                  <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                    <path d="M7 2v9M3.2 7.6 7 11.4 10.8 7.6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
                  </svg>
                </button>
              )}
            </div>
            {/* Detail panel anchored to the page-level column, so it spans the
                full height including the Events title — same as the workers view. */}
            {detailEvt && view === 'events' && (
              isMobile ? (
                <MobileDetailPanel>
                  <EventDetail evt={detailEvt} deliveries={deliveries} canGoBack={detailStack.length > 0} onBack={handleDetailBack} onFindPair={handleFindRequestPair} onClose={() => { setSelectedEventId(null); setDetailEvt(null) }} />
                </MobileDetailPanel>
              ) : (
                <ResizablePanel width={detailWidth} minWidth={DETAIL_MIN_WIDTH} onWidthChange={handlePanelResize}>
                  <EventDetail evt={detailEvt} deliveries={deliveries} canGoBack={detailStack.length > 0} onBack={handleDetailBack} onFindPair={handleFindRequestPair} onClose={() => { setSelectedEventId(null); setDetailEvt(null) }} />
                </ResizablePanel>
              )
            )}
          </>
        )}
      </div>

      {/* In-place worker detail overlay (opened from talk via right-click), so
          the detail pops up over the current view instead of navigating away. */}
      {(() => {
        const w = detailOverlayId ? workers.find(x => x.id === detailOverlayId) : undefined
        if (!w) return null
        return (
          <div
            onClick={() => setDetailOverlayId(null)}
            style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.45)', zIndex: 300, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: 16 }}
          >
            <div
              onClick={e => e.stopPropagation()}
              style={{ width: 'min(780px, 100%)', height: 'min(86vh, 660px)', display: 'flex', flexDirection: 'column', background: colors.bgLight, border: '1px solid ' + colors.border, borderRadius: 8, boxShadow: '0 8px 24px rgba(0,0,0,0.25)', overflow: 'hidden' }}
            >
              <WorkerDetail
                worker={w}
                allWorkers={workers}
                watch={workerWatch[w.id] ?? []}
                onClose={() => setDetailOverlayId(null)}
                archived={archived}
                onToggleArchived={toggleArchived}
                onDeleted={(id) => { setDetailOverlayId(null); refreshWorkers() }}
                onRefresh={refreshWorkers}
                busPort={context.bus_port}
              />
            </div>
          </div>
        )
      })()}
    </div>
  )
}
