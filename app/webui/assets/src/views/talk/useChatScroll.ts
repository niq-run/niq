// useChatScroll owns the scroll container and everything that drives it: the
// follow-to-bottom switch, the mount pin, the follow loop, pagination (short
// auto-fill + nearTop prefetch) with its prepend anchor, and the jump-to-bottom
// / jump-to-event helpers. Extracted from TalkView so the main component is
// about rendering.
//
// The switch is purely geometric: the reader is either within AT_BOTTOM_PX of
// the end (follow new content, no jump button) or above it (reading history,
// button shown). One threshold, no direction test — "am I near the bottom"
// answers both questions the old gesture bookkeeping tried to infer.
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

// AT_BOTTOM_PX is "the reader is at the end", in px. Sub-pixel layout leaves
// scrollTop a fraction short of the maximum, so it is a window rather than 0.
export const AT_BOTTOM_PX = 48

// The scroller's class, exported so the rows can find it without a prop chain
// (they anchor a folded row against it). One definition, no string drift.
export const TALK_SCROLL_CLASS = 'talk-scroll'

export function useChatScroll({ relevantEvents, events, onLoadMore, scrollToBottomSignal, onFollowChange }: {
  relevantEvents: EventPayload[]
  events: EventPayload[]
  onLoadMore?: () => void
  scrollToBottomSignal?: number
  onFollowChange?: (following: boolean) => void
}) {
  const scrollRef = useRef<HTMLDivElement>(null)
  const atBottomRef = useRef(true)
  // While the container scrolls itself (smooth jump to bottom / to an event),
  // the scroll events it produces must not be read as the reader moving.
  const programmaticUntilRef = useRef(0)
  // scrollHeight as of the last scroll event, so a scroll that only exists
  // because the content shrank (a collapsed block clamping scrollTop) can be
  // told apart from the reader actually moving towards the top.
  const lastHeightRef = useRef(0)
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
  // One writer for the switch. The owner is told every time (cheap, and it
  // keeps App's trim gate in sync across view switches); the ref/state only
  // move when the value actually changes, so scrolling stays render-free.
  const setFollow = useCallback((v: boolean) => {
    onFollowChangeRef.current?.(v)
    if (atBottomRef.current === v) return
    atBottomRef.current = v
    setAtBottom(v)
  }, [])
  const isAtEnd = (el: HTMLElement) => el.scrollHeight - el.scrollTop - el.clientHeight <= AT_BOTTOM_PX
  // The one place that moves the view to the tail: adopted by the pre-paint
  // layout effect and the follow loop alike, so both agree on "following" and
  // neither can pin while a gesture is in flight.
  const pinIfFollowing = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    if (atBottomRef.current && !manualRef.current) el.scrollTop = el.scrollHeight
    // lastHeightRef is deliberately NOT refreshed here: it must still hold the
    // height from before this commit, or the shrink clamp's own scroll event
    // would look like the reader moving and page history in.
    setFollow(isAtEnd(el))
  }, [setFollow])

  // Instant tail jump: the send signal and the mount pin both want "be at the
  // newest row now", without an animation to read scroll events from.
  const jumpToBottom = useCallback(() => {
    setFollow(true)
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    lastHeightRef.current = el.scrollHeight
  }, [setFollow])

  const lastScrollSignal = useRef(scrollToBottomSignal)
  useEffect(() => {
    if (scrollToBottomSignal === lastScrollSignal.current) return
    lastScrollSignal.current = scrollToBottomSignal
    jumpToBottom()
  }, [scrollToBottomSignal, jumpToBottom])

  // Mount pin: land at the newest row before the first paint.
  useLayoutEffect(() => {
    jumpToBottom()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Pre-paint pin: a commit that changes the timeline is pinned before the
  // browser paints, so the tail never shows a one-frame "kick then settle".
  // pinIfFollowing also re-reads the distance, which keeps the jump-to-bottom
  // button honest when a block grew after the scroll settled.
  useLayoutEffect(() => {
    pinIfFollowing()
  }, [relevantEvents, events])

  // Follow-to-bottom for layout changes that happen outside a React commit:
  // an image finishing decode, a font swapping in, the input box growing under
  // the scroller. Commits are already pinned by the layout effect; this loop
  // only writes scrollTop when one of the two boxes actually changed size, so
  // a quiet stretch costs two property reads per frame and nothing else.
  useEffect(() => {
    let raf = 0
    const sc0 = scrollRef.current
    let lastHeight = sc0?.scrollHeight ?? 0
    let lastClient = sc0?.clientHeight ?? 0
    const tick = () => {
      raf = requestAnimationFrame(tick)
      const sc = scrollRef.current
      if (!sc) return
      // Both boxes: content growth (streaming) and viewport changes (the input
      // box growing under focus, a window resize) move the bottom, and only the
      // former shows up in scrollHeight. Missing the second is what let a click
      // into the input nudge the conversation.
      if (sc.scrollHeight === lastHeight && sc.clientHeight === lastClient) return
      lastHeight = sc.scrollHeight
      lastClient = sc.clientHeight
      pinIfFollowing()
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [pinIfFollowing])

  const loadMoreRef = useRef(onLoadMore)
  loadMoreRef.current = onLoadMore

  const prependAnchorRef = useRef<{ top: number; height: number } | null>(null)
  const captureTopAnchor = () => {
    if (atBottomRef.current) return
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
    const height = el.scrollHeight
    const shrank = height < lastHeightRef.current
    lastHeightRef.current = height
    // A self-driven smooth scroll (button, quote jump) fires scroll events too;
    // the flag is already set for it, so ignore them until it lands.
    if (Date.now() >= programmaticUntilRef.current) setFollow(isAtEnd(el))
    // Near-top history prefetch: fire whenever the reader is resting in the top
    // zone (no scroll-direction requirement, so sitting at the true top still
    // loads each page) and the list actually overflows. A single in-flight latch
    // blocks re-entry, and the prepend re-pin raises scrollTop back above the
    // zone on a normal page, so it can't loop.
    //
    // Not when the content just shrank: collapsing a tall block clamps scrollTop
    // towards the top and fires a scroll event, which used to look like the
    // reader arriving at the top — one collapse could page history in (and push
    // the viewport far above the block that was folded).
    if (!shrank && el.scrollTop <= LOAD_EARLY_PX && height > el.clientHeight + 1) {
      maybeLoadMoreRef.current?.('nearTop')
    }
  }, [])

  const scrollToBottom = useCallback(() => {
    setFollow(true)
    programmaticUntilRef.current = Date.now() + 600
    const el = scrollRef.current
    if (el) el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  }, [setFollow])

  const scrollToEvent = useCallback((evtId: string) => {
    const el = scrollRef.current
    if (!el) return
    const target = el.querySelector(`[data-evt-id="${CSS.escape(evtId)}"]`) as HTMLElement | null
    if (!target) return
    const top = target.getBoundingClientRect().top - el.getBoundingClientRect().top + el.scrollTop - 12
    // Jumping to a row that is effectively the last one is still "at the end";
    // stopping follow there would leave the jump button showing.
    setFollow(top >= el.scrollHeight - el.clientHeight - AT_BOTTOM_PX)
    programmaticUntilRef.current = Date.now() + 600
    el.scrollTo({ top, behavior: 'smooth' })
  }, [setFollow])

  // 'short' auto-fill: re-evaluate after rows change whether the timeline fills
  // the container and top up if not (bounded).
  useEffect(() => {
    if (!onLoadMore || events.length === 0) return
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

