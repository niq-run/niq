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

// Order worker rows with the pinned ones first, preserving each group's
// relative order. Non-pinned workers keep their incoming order.
export function orderWithPinned<T>(items: T[], idOf: (t: T) => string): T[] {
  const pinned = new Set(readSet(PINNED_KEY))
  const top: T[] = []
  const rest: T[] = []
  for (const it of items) {
    if (pinned.has(idOf(it))) top.push(it)
    else rest.push(it)
  }
  return [...top, ...rest]
}