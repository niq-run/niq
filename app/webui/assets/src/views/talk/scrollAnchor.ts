// Keeping a scrolled viewport steady when content changes above it.
//
// The previous approach remembered scrollTop/scrollHeight and added the height
// delta back. That breaks as soon as anything else changes height in the same
// commit: one live row appended at the bottom poisons the delta, the anchor is
// spent, and when the history page finally lands there is no compensation left —
// the reader is thrown a whole page up. It also had to guess which commit was
// "the" one, so it was consumed by the first render after capture.
//
// Anchoring on a row that is on screen has neither problem: the row's viewport
// offset only moves when content ABOVE it changes, so the measurement is exact
// and reconciling is idempotent — it can run after every commit and do nothing
// until the content arrives. Whether the scroll position moved by itself tells
// us the reader scrolled, so a correction never fights them (which matters most
// on touch, where momentum keeps the list moving after the finger lifts).
export interface ScrollAnchor {
  row: Element
  // The row's viewport offset when it was last reconciled.
  top: number
  // The scroller's scrollTop at the same moment: a different value now means
  // the reader (or a jump) moved, not the content above the row.
  scrollTop: number
  // Reconcile only until then, so a request that never returns cannot pin the
  // view to a stale row forever.
  deadline: number
}

// HOLD_MS bounds one anchor's life. History pages and expansions land in well
// under a second locally; this only guards against a lost response.
const HOLD_MS = 3000

// captureScrollAnchor anchors on the first row matching selector inside the
// scroller — both lists key their rows, so the element survives a prepend.
export function captureScrollAnchor(scroller: HTMLElement | null, selector: string): ScrollAnchor | null {
  return captureElementAnchor(scroller?.querySelector(selector) ?? null, scroller)
}

// captureElementAnchor does the same for an element already in hand (a folded
// group's chip, which is not a row).
export function captureElementAnchor(row: Element | null, scroller: HTMLElement | null): ScrollAnchor | null {
  if (!scroller || !row) return null
  return { row, top: row.getBoundingClientRect().top, scrollTop: scroller.scrollTop, deadline: Date.now() + HOLD_MS }
}

// settleScrollAnchor puts the anchored row back where it was, and reports
// whether the anchor is still worth holding (false once the row is gone or the
// wait expired). Call it after every commit; with nothing changed it costs one
// measurement and writes nothing.
export function settleScrollAnchor(scroller: HTMLElement | null, a: ScrollAnchor): boolean {
  if (!scroller || !a.row.isConnected || Date.now() > a.deadline) return false
  const rowTop = a.row.getBoundingClientRect().top
  const delta = rowTop - a.top
  if (delta === 0) return true
  if (scroller.scrollTop === a.scrollTop) {
    // scrollTop unchanged since the last look: the content above moved, so move
    // the viewport by the same amount and leave the anchor where it is.
    scroller.scrollTop += delta
  } else {
    // The reader moved. Their position wins: re-anchor onto the row where they
    // left it instead of pulling them back.
    a.top = rowTop
  }
  a.scrollTop = scroller.scrollTop
  return true
}
