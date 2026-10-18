// Shared localStorage-backed worker list preferences for the worker pickers
// (sidebar selector and the expanded worker modal), plus ordering helpers.
// Pinning sticks a worker to the top of the picker list; sleeping hides it from
// the sidebar's quick inline list (it still surfaces in the modal's slept group
// so it stays reversible). Both persist as ordered id arrays under their own
// localStorage keys.
const PINNED_KEY = 'niq.pinned-workers'
const SLEPT_KEY = 'niq.slept-workers'

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

// Order worker rows with the pinned ones first. The pinned group follows the
// PERSISTED pinned order (the array order), so it is stable across renders and
// reorderable by dragging; non-pinned workers keep their incoming order.
export function orderWithPinned<T>(items: T[], idOf: (t: T) => string): T[] {
  const order = readSet(PINNED_KEY)
  const byId = new Map(items.map((it) => [idOf(it), it]))
  const top: T[] = []
  for (const id of order) {
    const it = byId.get(id)
    if (it !== undefined) top.push(it)
  }
  const pinned = new Set(order)
  const rest = items.filter((it) => !pinned.has(idOf(it)))
  return [...top, ...rest]
}

// movePinnedWorker reorders the pinned group: drop `dragId` immediately before
// (or after) `targetId`. No-op unless both are pinned. Returns the new order.
export function movePinnedWorker(dragId: string, targetId: string, before: boolean): string[] {
  const cur = readSet(PINNED_KEY)
  if (dragId === targetId) return cur
  const from = cur.indexOf(dragId)
  if (from < 0 || cur.indexOf(targetId) < 0) return cur
  const next = cur.slice()
  next.splice(from, 1)
  const targetIdx = next.indexOf(targetId)
  next.splice(before ? targetIdx : targetIdx + 1, 0, dragId)
  try {
    localStorage.setItem(PINNED_KEY, JSON.stringify(next))
  } catch { /* quota / disabled storage */ }
  return next
}