// Shared localStorage-backed worker list preferences for the worker pickers
// (sidebar selector and the expanded worker modal), plus ordering helpers.
// Pinning sticks a worker to the top of the picker list; sleeping hides it from
// the sidebar's quick inline list (it still surfaces in the modal's slept group
// so it stays reversible). Both persist as ordered id arrays under their own
// localStorage keys.
const PINNED_KEY = 'niq.pinned-workers'
const SLEPT_KEY = 'niq.slept-workers'
// The user's display order for worker rows (a flat id list, drag-reorderable).
// Pinning decides GROUP membership (pinned rows render first); this decides the
// order WITHIN each group.
const ORDER_KEY = 'niq.worker-order'

function readSet(key: string): string[] {
  try {
    const raw = localStorage.getItem(key)
    if (!raw) return []
    const arr = JSON.parse(raw)
    if (!Array.isArray(arr)) return []
    return arr.filter((x): x is string => typeof x === 'string')
  } catch {
    return []
  }
}

// Returns the new array after toggling an id in the set persisted at `key`.
function toggleInSet(key: string, id: string, on: boolean): string[] {
  const cur = readSet(key)
  const next = on ? (cur.includes(id) ? cur : [...cur, id]) : cur.filter(x => x !== id)
  try {
    localStorage.setItem(key, JSON.stringify(next))
  } catch { /* quota / disabled storage — the pref just won't persist */ }
  return next
}

// ── Pin ──
export function getPinnedWorkers(): string[] { return readSet(PINNED_KEY) }
export function setPinnedWorker(id: string, pinned: boolean): string[] {
  return toggleInSet(PINNED_KEY, id, pinned)
}
export function isPinned(id: string): boolean { return readSet(PINNED_KEY).includes(id) }

// ── Sleep ──
export function getSleptWorkers(): string[] { return readSet(SLEPT_KEY) }
export function setSleptWorker(id: string, slept: boolean): string[] {
  return toggleInSet(SLEPT_KEY, id, slept)
}
export function isSlept(id: string): boolean { return readSet(SLEPT_KEY).includes(id) }

// The persisted display order of worker rows (ids the user has positioned).
export function getWorkerOrder(): string[] { return readSet(ORDER_KEY) }

// Order worker rows: pinned ones first (pinning decides GROUP membership), then
// the rest. WITHIN each group the order is the persisted ORDER_KEY order, so any
// row can be dragged to a new position and the result survives a reload. Ids
// with no saved position keep their incoming order and sink to the end of their
// group.
export function orderWithPinned<T>(items: T[], idOf: (t: T) => string): T[] {
  const rank = new Map(readSet(ORDER_KEY).map((id, i) => [id, i]))
  const pinnedSet = new Set(readSet(PINNED_KEY))
  const incoming = new Map(items.map((it, i) => [idOf(it), i]))
  const byId = new Map(items.map((it) => [idOf(it), it]))

  const ids = [...byId.keys()].sort((a, b) => {
    const ra = rank.get(a)
    const rb = rank.get(b)
    if (ra !== undefined && rb !== undefined) return ra - rb
    if (ra !== undefined) return -1
    if (rb !== undefined) return 1
    return (incoming.get(a) ?? 0) - (incoming.get(b) ?? 0)
  })
  const ordered = ids.map((id) => byId.get(id)!).filter(Boolean)
  const pinnedRows = ordered.filter((it) => pinnedSet.has(idOf(it)))
  const restRows = ordered.filter((it) => !pinnedSet.has(idOf(it)))
  return [...pinnedRows, ...restRows]
}

// moveWorkerOrder records that `dragId` should sit immediately before (or after)
// `targetId` in the display order, and persists it. Works for ANY row, pinned or
// not. Returns the new order.
export function moveWorkerOrder(
  dragId: string,
  targetId: string,
  before: boolean,
  visible: string[] = [],
): string[] {
  if (dragId === targetId) return readSet(ORDER_KEY)
  // Seed the order with what is on screen right now (then whatever was already
  // saved), so the FIRST drag does not push unranked siblings to arbitrary
  // positions: on-screen order becomes the baseline, then we move one row.
  const base: string[] = []
  const seen = new Set<string>()
  for (const id of [...readSet(ORDER_KEY), ...visible]) {
    if (seen.has(id)) continue
    seen.add(id)
    base.push(id)
  }
  const next = base.filter((id) => id !== dragId)
  const targetIdx = next.indexOf(targetId)
  if (targetIdx < 0) {
    next.push(dragId)
  } else {
    next.splice(before ? targetIdx : targetIdx + 1, 0, dragId)
  }
  try {
    localStorage.setItem(ORDER_KEY, JSON.stringify(next))
  } catch { /* quota / disabled storage */ }
  return next
}