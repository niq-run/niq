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
import { captureScrollAnchor, settleScrollAnchor, type ScrollAnchor } from './scrollAnchor'

// MAX_TALK_BACKFILL caps how many history pages the talk view will page back
// through when its timeline is empty (recent events all filtered out). Bounds
// the backfill so a project with no real conversation can't spin.
const MAX_TALK_BACKFILL = 30

// NEAR_TOP_PX caps the prefetch distance. It is scaled down to a third of the
// viewport: a flat 480px is most of a phone's height, so the smallest flick
// already paged history in.
const NEAR_TOP_PX = 480

// nearTopPx is how close to the top the next page is pulled in. Exported so the
// events list uses the same distance.
export function nearTopPx(el: HTMLElement): number {
  return Math.min(NEAR_TOP_PX, Math.round(el.clientHeight / 3))
}

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
    setFollow(isAtEnd(el))
  }, [setFollow])

  // Instant tail jump: the send signal and the mount pin both want "be at the
  // newest row now", without an animation to read scroll events from.
  const jumpToBottom = useCallback(() => {
    setFollow(true)
    prependAnchorRef.current = null
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
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

  // The row the reader is looking at, held across a history prepend (and across
  // the live appends that race it). See scrollAnchor.ts for why the anchor is a
  // row rather than a scrollHeight snapshot.
  const prependAnchorRef = useRef<ScrollAnchor | null>(null)
  const captureTopAnchor = () => {
    if (atBottomRef.current) return
    prependAnchorRef.current = captureScrollAnchor(scrollRef.current, '[data-evt-id]')
  }

  // One rule for paging history: pull the next page whenever the top of the
  // timeline is within nearTopPx() of the viewport. A timeline shorter than the
  // screen has scrollTop 0, so the same condition covers it — there is no
  // separate "fill the screen" pass. Re-evaluated on scroll and after every
  // commit; one in-flight latch stops re-entry while a page is on its way.
  const loadingRef = useRef(false)
  const fillCountRef = useRef(0)
  const maybeLoadMore = () => {
    const sc = scrollRef.current
    if (!sc || !loadMoreRef.current || loadingRef.current) return
    const short = sc.scrollHeight <= sc.clientHeight + 1
    if (!short) fillCountRef.current = 0
    if (sc.scrollTop > nearTopPx(sc)) return
    // A timeline that never fills up (every recent event filtered out of the
    // conversation) is topped up a bounded number of pages, never forever.
    if (short) {
      if (fillCountRef.current >= MAX_TALK_BACKFILL) return
      fillCountRef.current++
    }
    loadingRef.current = true
    captureTopAnchor()
    loadMoreRef.current()
    window.setTimeout(() => { loadingRef.current = false }, 400)
  }
  const maybeLoadMoreRef = useRef(maybeLoadMore)
  maybeLoadMoreRef.current = maybeLoadMore

  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return
    // Absorb the reader's own movement into the anchor as it happens, so a
    // page that lands mid-gesture (touch momentum especially) is still measured
    // against the position they just came to.
    const anchor = prependAnchorRef.current
    if (anchor && !settleScrollAnchor(el, anchor)) prependAnchorRef.current = null
    // A self-driven smooth scroll (button, quote jump) fires scroll events too;
    // the flag is already set for it, so ignore them until it lands.
    if (Date.now() >= programmaticUntilRef.current) setFollow(isAtEnd(el))
    maybeLoadMoreRef.current?.()
  }, [])

  const scrollToBottom = useCallback(() => {
    setFollow(true)
    prependAnchorRef.current = null
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

  // Reconcile the prepend anchor after every commit rather than on a guessed
  // one: a live append leaves the anchor row untouched (nothing to do), and the
  // history page moves it exactly once, when it lands. Until then the anchor is
  // kept; once it has done its job it is dropped.
  useLayoutEffect(() => {
    const a = prependAnchorRef.current
    if (a && !settleScrollAnchor(scrollRef.current, a)) prependAnchorRef.current = null
  })

  // Then re-evaluate the top of the timeline: a live append, a landed page or a
  // collapsed block can all move it into or out of the zone, and a timeline
  // shorter than the screen never scrolls at all. Ordered after the anchor so a
  // pending prepend is settled before a new one is captured.
  useLayoutEffect(() => {
    if (events.length > 0) maybeLoadMoreRef.current?.()
  })

  return { scrollRef, atBottom, handleScroll, markManual, scrollToBottom, scrollToEvent }
}

