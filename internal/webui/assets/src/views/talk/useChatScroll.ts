// useChatScroll owns the scroll container and everything that drives it: the
// sticky follow-to-bottom switch (on by default, off on an explicit up-scroll,
// back on at the bottom / via the button / a send), the manual-gesture detector
// that distinguishes user scrolls from the follow loop's programmatic writes,
// the mount pin, the follow loop, pagination (short auto-fill + nearTop
// prefetch) with its prepend anchor, and the jump-to-bottom / jump-to-event
// helpers. Extracted from TalkView so the main component is about rendering.
import { useRef, useEffect, useLayoutEffect, useCallback, useState } from 'react'
import type { EventPayload } from '../../types'

// MAX_TALK_BACKFILL caps how many history pages the talk view will page back
// through when its timeline is empty (recent events all filtered out). Bounds
// the backfill so a project with no real conversation can't spin.
const MAX_TALK_BACKFILL = 30

// LOAD_EARLY_PX is how far below the top the view starts prefetching older
// events, so the historical page lands while there is still room to scroll
// instead of stalling exactly at the earliest message.
const LOAD_EARLY_PX = 480

// ── Scrolling / follow / pagination ──
// Owns the scroll container and everything that drives it: the sticky
// follow-to-bottom switch (on by default, off on an explicit up-scroll, back on
// at the bottom / via the button / a send), the manual-gesture detector that
// distinguishes user scrolls from the follow loop's programmatic writes, the
// mount pin, the follow loop, the pagination (short auto-fill + nearTop
// prefetch) with its prepend anchor, and the jump-to-bottom / jump-to-event
// helpers. Extracted from TalkView so the main body is about rendering, not
// scroll bookkeeping.
export function useChatScroll({ relevantEvents, events, onLoadMore, scrollToBottomSignal, onFollowChange }: {
  relevantEvents: EventPayload[]
  events: EventPayload[]
  onLoadMore?: () => void
  scrollToBottomSignal?: number
  onFollowChange?: (following: boolean) => void
}) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const autoScrollRef = useRef(true)
  const prevScrollRef = useRef(0)
  const manualRef = useRef(false)
  const manualTimerRef = useRef(0)
  const markManual = useCallback(() => {
    manualRef.current = true
    window.clearTimeout(manualTimerRef.current)
    manualTimerRef.current = window.setTimeout(() => { manualRef.current = false }, 400)
  }, [])
  const onFollowChangeRef = useRef(onFollowChange)
  onFollowChangeRef.current = onFollowChange
  const [atBottom, setAtBottom] = useState(true)

  const lastScrollSignal = useRef(scrollToBottomSignal)
  useEffect(() => {
    if (scrollToBottomSignal === lastScrollSignal.current) return
    lastScrollSignal.current = scrollToBottomSignal
    autoScrollRef.current = true
    setAtBottom(true)
    onFollowChangeRef.current?.(true)
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [scrollToBottomSignal])

  // Mount pin: land at the newest row before the first paint.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (el && autoScrollRef.current) el.scrollTop = el.scrollHeight
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Synchronous bottom pin on content growth. The rAF loop below is a whole
  // frame behind a render: by the time it sees the new (taller) scrollHeight
  // and re-pins, the browser has already painted one frame with the stale
  // scrollTop — the visible "kick then settle" that reads as a jump whenever
  // the whole tail fits in the viewport (e.g. collapsed/header-only thinking
  // blocks are short constant-height rows, so every stream batch shows the
  // kick; a very tall block above the viewport hides it). Pinning in a layout
  // effect runs before paint, so the frame already shows the pinned bottom.
  // Only when the follow switch is on, and only when we aren't already there.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el || !autoScrollRef.current) return
    const max = el.scrollHeight - el.clientHeight
    if (el.scrollTop !== max) el.scrollTop = max
  }, [relevantEvents, events])

  // Follow-to-bottom on content growth: a rAF loop that only writes scrollTop
  // when the height actually changed, so a quiet stretch never locks the
  // scroller and both a sent message and a stream batch are followed to the
  // bottom while the switch is on. It keeps pace with the GrowingHeight CSS
  // transition (which changes scrollHeight over several frames, not in the
  // single commit the layout effect above sees).
  useEffect(() => {
    let raf = 0
    let lastHeight = scrollRef.current?.scrollHeight ?? 0
    const tick = () => {
      raf = requestAnimationFrame(tick)
      const sc = scrollRef.current
      if (!sc) return
      const h = sc.scrollHeight
      if (h === lastHeight) return
      lastHeight = h
      if (autoScrollRef.current) sc.scrollTop = sc.scrollHeight
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [])

  const loadMoreRef = useRef(onLoadMore)
  loadMoreRef.current = onLoadMore

  const prependAnchorRef = useRef<{ id: string; offset: number } | null>(null)
  const captureTopAnchor = () => {
    if (autoScrollRef.current) return
    const el = scrollRef.current
    if (!el) return
    const nodes = el.querySelectorAll<HTMLElement>('[data-evt-id]')
    let pick: HTMLElement | null = null
    let pickTop = Infinity
    for (let i = 0; i < nodes.length; i++) {
      const n = nodes[i] as HTMLElement
      const nt = n.getBoundingClientRect().top
      if (nt < pickTop) { pickTop = nt; pick = n }
    }
    const id = pick?.dataset?.evtId
    if (!pick || !id) return
    const ct = el.getBoundingClientRect().top
    prependAnchorRef.current = { id, offset: pickTop - ct }
  }

  // Unified load-more controller: 'short' (timeline doesn't fill the container)
  // and 'nearTop' (user scrolled near the top) both funnel here, behind one
  // in-flight latch + backfill cap, and pre-capture the prepend anchor.
  const loadingRef = useRef(false)
  const backfillCountRef = useRef(0)
  const maybeLoadMore = (reason: 'short' | 'nearTop') => {
    const sc = scrollRef.current
    if (!sc || !loadMoreRef.current) return
    const need = reason === 'short'
      ? sc.scrollHeight <= sc.clientHeight + 1
      : sc.scrollTop < LOAD_EARLY_PX
    if (!need) {
      if (reason === 'short') backfillCountRef.current = 0
      return
    }
    if (reason === 'short' && backfillCountRef.current >= MAX_TALK_BACKFILL) return
    if (loadingRef.current) return
    loadingRef.current = true
    if (reason === 'short') backfillCountRef.current++
    else backfillCountRef.current = 0
    captureTopAnchor()
    loadMoreRef.current()
    setTimeout(() => { loadingRef.current = false }, 400)
  }
  const maybeLoadMoreRef = useRef(maybeLoadMore)
  maybeLoadMoreRef.current = maybeLoadMore

  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    const dist = el.scrollHeight - el.scrollTop - el.clientHeight
    const prev = prevScrollRef.current
    // Sticky follow switch: off only on an explicit up-scroll; content growth
    // never flips it (the follow loop only raises scrollTop and the trim gate
    // means nothing shrinks above). Back on only when the user scrolls down
    // into the bottom window, or via the button / a send.
    if (autoScrollRef.current && manualRef.current && el.scrollTop < prev) {
      autoScrollRef.current = false
    } else if (!autoScrollRef.current && el.scrollTop > prev && dist < 50) {
      autoScrollRef.current = true
    }
    prevScrollRef.current = el.scrollTop
    setAtBottom(autoScrollRef.current)
    onFollowChangeRef.current?.(autoScrollRef.current)
    maybeLoadMoreRef.current?.('nearTop')
  }, [])

  const scrollToBottom = useCallback(() => {
    autoScrollRef.current = true
    setAtBottom(true)
    onFollowChangeRef.current?.(true)
    const el = scrollRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }, [])

  const scrollToEvent = useCallback((evtId: string) => {
    const el = scrollRef.current
    if (!el) return
    const target = el.querySelector(`[data-evt-id="${CSS.escape(evtId)}"]`) as HTMLElement | null
    if (!target) return
    autoScrollRef.current = false
    onFollowChangeRef.current?.(false)
    const top = target.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop - 12
    el.scrollTo({ top, behavior: 'smooth' })
  }, [])

  // 'short' auto-fill: re-evaluate after rows change whether the timeline fills
  // the container and top up if not (bounded).
  useEffect(() => {
    if (!onLoadMore || events.length === 0) return
    loadingRef.current = false
    maybeLoadMoreRef.current?.('short')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [relevantEvents])

  // If a history prepend is in flight, re-pin the anchored node to its offset.
  useLayoutEffect(() => {
    const a = prependAnchorRef.current
    if (!a) return
    prependAnchorRef.current = null
    const el = scrollRef.current
    if (!el) return
    const target = el.querySelector<HTMLElement>(`[data-evt-id="${CSS.escape(a.id)}"]`)
    if (!target) return
    const offNow = target.getBoundingClientRect().top - el.getBoundingClientRect().top
    const delta = offNow - a.offset
    if (delta !== 0) el.scrollTop += delta
  }, [relevantEvents])

  return { scrollRef, atBottom, handleScroll, markManual, scrollToBottom, scrollToEvent }
}

