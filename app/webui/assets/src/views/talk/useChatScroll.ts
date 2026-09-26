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
  // Previous distance-to-bottom, used to re-engage follow only when the reader
  // is actually moving toward (or already at) the bottom — see handleScroll.
  const prevDistRef = useRef(Number.POSITIVE_INFINITY)
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
  // Only when the follow switch is on, NOT while the user is mid-gesture
  // (manualRef lets an up-scroll actually move and disengage follow — the
  // follow re-pin used to fight the scroll, leaving follow stuck on and
  // yanking the reader back to the bottom seconds later), and only when we
  // aren't already there.
  useLayoutEffect(() => {
    const el = scrollRef.current
    if (!el || !autoScrollRef.current || manualRef.current) return
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
      // Pause while the user is gesturing so an up-scroll can take effect and
      // turn follow off (otherwise this loop holds the bottom and follow never
      // disengages).
      if (autoScrollRef.current && !manualRef.current) sc.scrollTop = sc.scrollHeight
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [])

  const loadMoreRef = useRef(onLoadMore)
  loadMoreRef.current = onLoadMore

  const prependAnchorRef = useRef<{ top: number; height: number } | null>(null)
  const captureTopAnchor = () => {
    if (autoScrollRef.current) return
    const el = scrollRef.current
    if (!el) return
    prependAnchorRef.current = { top: el.scrollTop, height: el.scrollHeight }
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
      : sc.scrollTop <= LOAD_EARLY_PX
    if (!need) {
      if (reason === 'short') backfillCountRef.current = 0
      return
    }
    if (reason === 'short' && backfillCountRef.current >= MAX_TALK_BACKFILL) return
    // One in-flight load at a time. An idle reader resting at the very top is
    // still served: the prepend re-pin raises scrollTop back above LOAD_EARLY_PX
    // for a normal-sized page, and while a load is pending this latch blocks the
    // re-pin's own scroll event, so there is no runaway loop — no per-gesture
    // direction gate or hard page cap is needed (mirroring the events view).
    if (loadingRef.current) return
    loadingRef.current = true
    if (reason === 'short') backfillCountRef.current++
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
    // Sticky follow switch, GESTURE-BASED (mirrors the events view): off on an
    // explicit up-scroll (a real gesture, not the follow-loop's programmatic
    // writes — marked by manualRef, with a small 4px deadband so trackpad/natural
    // micro-jitter while pinned doesn't drop it and flash the jump-to-bottom
    // button), back on once the reader scrolls down into the 50px bottom window.
    // Content growth never flips it, so a message pushing the bottom past the old
    // threshold can't accidentally disengage. After a switch change the previous
    // distance feeds the re-engage test.
    const prev = prevScrollRef.current
    if (autoScrollRef.current && manualRef.current && el.scrollTop < prev - 4) {
      autoScrollRef.current = false
    } else if (!autoScrollRef.current && dist < 50 && dist <= prevDistRef.current) {
      autoScrollRef.current = true
    }
    prevScrollRef.current = el.scrollTop
    prevDistRef.current = dist
    setAtBottom(autoScrollRef.current)
    onFollowChangeRef.current?.(autoScrollRef.current)
    // Near-top history prefetch: fire whenever the reader is resting in the top
    // zone (no scroll-direction requirement, so sitting at the true top still
    // loads each page) and the list actually overflows. A single in-flight latch
    // blocks re-entry, and the prepend re-pin raises scrollTop back above the
    // zone on a normal page, so it can't loop.
    if (el.scrollTop <= LOAD_EARLY_PX && el.scrollHeight > el.clientHeight + 1) {
      maybeLoadMoreRef.current?.('nearTop')
    }
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

  // If a history prepend is in flight, keep the same viewport region anchored:
  // content grew by `grew` px above what the reader was looking at, so raise
  // scrollTop by exactly that much. Unconditional (even when the reader is
  // mid-gesture or at the true top) so a prepend never reads as a jump.
  useLayoutEffect(() => {
    const a = prependAnchorRef.current
    if (!a) return
    prependAnchorRef.current = null
    const el = scrollRef.current
    if (!el) return
    const grew = el.scrollHeight - a.height
    if (grew > 0) el.scrollTop = a.top + grew
  }, [relevantEvents])

  return { scrollRef, atBottom, handleScroll, markManual, scrollToBottom, scrollToEvent }
}

