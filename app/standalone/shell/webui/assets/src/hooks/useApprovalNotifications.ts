import { useCallback, useRef, useState } from 'react'
import { useI18n } from '../i18n'
import type { ApprovalEntry, ViewMode } from '../types'

// Where the opt-in is remembered across reloads.
const NOTIFY_KEY = 'niq-approval-notify'

// The browser's answer to "may this page show notifications?".
// 'unsupported' covers both a missing Notification API and a page served
// outside a secure context (the API exists there but never grants).
export type NotifyPermission = 'granted' | 'denied' | 'default' | 'unsupported'

function readPermission(): NotifyPermission {
  if (typeof window === 'undefined' || typeof Notification === 'undefined') return 'unsupported'
  if (!window.isSecureContext) return 'unsupported'
  return Notification.permission as NotifyPermission
}

function readOptIn(): boolean {
  try { return localStorage.getItem(NOTIFY_KEY) === '1' } catch { return false }
}

// Browser notifications for approval requests.
//
// The Notification API only exists in a secure context and needs an explicit
// permission grant, which browsers accept only from inside a user gesture —
// so the opt-in is a bell in the sidebar footer and this hook is the plumbing
// around it.
//
// The set of already-announced requests is per scope (the project the
// approvals belong to): switching projects must not announce the other
// project's backlog.
export function useApprovalNotifications(scope: string, view: ViewMode, onNavigate: () => void) {
  const { t } = useI18n()
  const [permission, setPermission] = useState<NotifyPermission>(() => readPermission())
  const [on, setOn] = useState(() => readOptIn())

  // The values the notify path reads, kept in refs so the announce callback
  // stays stable and the poll that calls it is never restarted.
  const tRef = useRef(t)
  tRef.current = t
  const onRef = useRef(on)
  onRef.current = on
  const permRef = useRef(permission)
  permRef.current = permission
  const viewRef = useRef(view)
  viewRef.current = view
  const navRef = useRef(onNavigate)
  navRef.current = onNavigate

  // The approval ids already announced, and the scope they belong to.
  // null until the first list arrives: that first list is the backlog
  // predating this page being open, so it is seeded silently rather than
  // announced.
  const seenRef = useRef<{ scope: string; ids: Set<string> } | null>(null)

  // Turning the feature on asks the browser for permission. The request must
  // run inside the click handler, so it is issued synchronously here — the
  // promise only resolves the outcome.
  const toggle = useCallback(() => {
    // Re-read: the user may have allowed (or blocked) notifications in the
    // browser's settings since the page loaded.
    const perm = readPermission()
    setPermission(perm)
    if (perm === 'granted') {
      const next = !onRef.current
      setOn(next)
      try {
        if (next) localStorage.setItem(NOTIFY_KEY, '1')
        else localStorage.removeItem(NOTIFY_KEY)
      } catch { /* ignore persistence failure */ }
      return
    }
    if (perm !== 'default') return
    Notification.requestPermission().then(p => {
      setPermission(p as NotifyPermission)
      if (p !== 'granted') return
      setOn(true)
      try { localStorage.setItem(NOTIFY_KEY, '1') } catch { /* ignore */ }
    }).catch(() => { /* the prompt was dismissed */ })
  }, [])

  // Called with every freshly polled list. Announces the requests that are
  // new since the last call.
  const announce = useCallback((list: ApprovalEntry[]) => {
    const seen = seenRef.current
    // A first list, or another project's: its backlog is not news — it is
    // seeded silently so only what arrives afterwards is announced.
    if (!seen || seen.scope !== scope) {
      seenRef.current = { scope, ids: new Set(list.map(a => a.id)) }
      return
    }
    const fresh = list.filter(a => !a.decision && !seen.ids.has(a.id))
    for (const a of list) seen.ids.add(a.id)
    if (fresh.length === 0) return
    if (!onRef.current || permRef.current !== 'granted') return
    // The user is looking straight at the list: no notification needed.
    if (viewRef.current === 'approvals' && document.hasFocus()) return
    for (const a of fresh) {
      showNotification(tRef.current('approvals.notify.title'), approvalBody(a), a.id, () => navRef.current())
    }
  }, [scope])

  return { permission, on, toggle, announce }
}

// The notification body: who asked, for what, and (for a mount request) the
// path that triggered it — the actionable part of the request.
function approvalBody(a: ApprovalEntry): string {
  const parts = [a.worker_id]
  if (a.action) parts.push(a.action)
  if (a.path) parts.push(a.path)
  return parts.join(' · ')
}

// Show one notification. Returns false when the browser refused — the API
// is unavailable outside a secure context, and some browsers only allow it
// from a service worker, in which case the constructor throws.
function showNotification(title: string, body: string, tag: string, onClick: () => void): boolean {
  if (typeof Notification === 'undefined') return false
  if (Notification.permission !== 'granted') return false
  try {
    const n = new Notification(title, { body, tag: 'niq-approval-' + tag })
    n.onclick = () => {
      // Bring the tab forward, then jump to the approvals list.
      window.focus()
      onClick()
      n.close()
    }
    return true
  } catch {
    return false
  }
}
