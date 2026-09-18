// ── Utility functions for TalkView / EventRow ──

import type { EventPayload } from '../types'

// ── Content extraction ──

/**
 * Split a message text into an optional `<system-reminder>…</system-reminder>`
 * block and the remaining user content. Messages without a reminder return
 * `{ reminder: null, content: text }`. The match is non-greedy so only the
 * first block is extracted.
 */
export function splitSystemReminder(
  text: string,
): { reminder: string | null; content: string } {
  const m = text.match(/<system-reminder>([\s\S]*?)<\/system-reminder>/)
  if (!m || m.index === undefined) {
    return { reminder: null, content: text }
  }
  const content = (text.slice(0, m.index) + text.slice(m.index + m[0].length)).replace(/^\n+|\n+$/g, '')
  return { reminder: m[1].trim(), content }
}

export function getContentText(evt: EventPayload): string {
  if (evt.type === 'reason.thinking' || evt.type === 'reason.response') {
    const content = evt.payload?.content
    return Array.isArray(content) ? content.filter(Boolean).join('\n') : ''
  }
  return ''
}

export function getInputText(evt: EventPayload): string {
  return (evt.payload?.text as string) || ''
}

// ── HIW attachment envelope ──

// ParsedAttachment is one <attachment> block found in an input text: an
// image carried inline as base64, or a file reference by path.
export interface ParsedAttachment {
  kind: 'image' | 'file'
  mime?: string
  data?: string // image: base64
  name?: string
  path?: string // file
  size?: number
}

const ATTACH_RE = /<attachment\b([^>]*?)(\/>|>([\s\S]*?)<\/attachment>)/g
const ATTR_RE = /([a-zA-Z_][\w-]*)="([^"]*)"/g

// parseAttachments splits an input text into the remaining plain text (blocks
// removed, trimmed) and the ordered attachment list. Malformed blocks stay in
// the text — same fail-open rule as the worker-side parser.
export function parseAttachments(text: string): { text: string; attachments: ParsedAttachment[] } {
  const attachments: ParsedAttachment[] = []
  const kept: string[] = []
  let pos = 0
  for (const m of text.matchAll(ATTACH_RE)) {
    const start = m.index ?? 0
    kept.push(text.slice(pos, start))
    pos = start + m[0].length
    const attrs: Record<string, string> = {}
    for (const a of m[1].matchAll(ATTR_RE)) attrs[a[1].toLowerCase()] = a[2]
    if (attrs.type === 'image' && m[3] && m[3].trim()) {
      attachments.push({ kind: 'image', mime: attrs.mime || 'image/png', data: m[3].replace(/\s+/g, ''), name: attrs.name })
    } else if (attrs.type === 'file' && attrs.path) {
      attachments.push({ kind: 'file', name: attrs.name || attrs.path, path: attrs.path, size: Number(attrs.size) || 0 })
    } else {
      kept.push(m[0]) // malformed: keep the raw block
    }
  }
  kept.push(text.slice(pos))
  return { text: kept.join('').trim(), attachments }
}

// Compose an attachment envelope block (the mirror of the parsers above); the
// composer in App appends these to the input text.
export function attachmentBlock(a: ParsedAttachment): string {
  if (a.kind === 'image') {
    return `<attachment type="image" mime="${a.mime}" name="${a.name}">\n${a.data}\n</attachment>`
  }
  return `<attachment type="file" name="${a.name}" path="${a.path}" size="${a.size}"/>`
}

// ── Type checks ──

export function isToolEvent(type: string): boolean {
  return type === 'request.completed' || type === 'request.failed' || type === 'request.rejected' || type === 'request.progressed' || type === 'request.cancel'
}

// isToolResult reports whether a type is a terminal answer to an invocation —
// the only events that carry the request_id of the call they answer. It is the
// pairing half of the request protocol: request.cancel / request.progressed
// are lifecycle events, not answers.
export function isToolResult(type: string): boolean {
  return type === 'request.completed' || type === 'request.failed' || type === 'request.rejected'
}

export function isReasonBoundary(type: string): boolean {
  return type === 'reason.start' || type === 'reason.end'
}

// isToolInvocation reports whether an event is a tool-call card: a domain-typed
// event (the capability's own name, e.g. "read", "bash", "elapse", or a meta
// extension like "context.compress") that the reason worker published with a
// request_id set, answered later by a request.* reply. Everything that has its
// own dedicated renderer (user input, reasoning output, timers, approvals,
// cancels) is NOT an invocation.
export function isToolInvocation(type: string): boolean {
  switch (type) {
    case 'worker.input':
    case 'worker.abort':
    case 'timer.reminder':
    case 'timer.timeout':
    case 'request.cancel':
    case 'reason.interrupted':
    case 'reason.thinking':
    case 'reason.response':
    case 'approval.request':
      return false
    default:
      return true
  }
}

// ── Tool helpers ──

// toolSummary is the card title: the event type, in brackets. Every event is
// rendered as a card, so there is nothing to classify — a capability is invoked
// under its own event type and the payload is the argument object itself.
export function toolSummary(evt: EventPayload): string {
  return `[${evt.type}]`
}

export function toolCallId(evt: EventPayload): string {
  return evt.request_id || ''
}

export function toolContent(evt: EventPayload, formatted: boolean): string {
  const format = (v: any): string =>
    formatted ? JSON.stringify(v, null, 2) : JSON.stringify(v)
  if (evt.type === 'request.progressed') {
    const partial = evt.payload?.partial
    if (typeof partial === 'string') return partial
    return ''
  }
  if (evt.type === 'request.completed') {
    const result = evt.payload?.result
    if (typeof result === 'string') {
      if (formatted) {
        try {
          const parsed = JSON.parse(result)
          return JSON.stringify(parsed, null, 2)
        } catch { /* not JSON, return as-is */ }
      }
      return result
    }
    if (result) return format(result)
    return ''
  }
  if (evt.type === 'request.failed') {
    const err = evt.payload?.error
    if (err) return String(err)
    return ''
  }
  if (evt.type === 'request.rejected') {
    const reason = evt.payload?.reason
    if (reason) return String(reason)
    return ''
  }
  // Any other event: the payload IS the argument object — there is no wrapper
  // to unwrap.
  const args = evt.payload
  if (!args || Object.keys(args).length === 0) return ''
  return format(args)
}

// ── Event type → color ──

export function getTypeColor(type: string, colors: import('../theme').Palette): string {
  if (type.startsWith('tool.') || type.startsWith('request.')) return colors.eventType.tool
  if (type.startsWith('reason.')) return colors.eventType.reason
  if (type.startsWith('worker.')) return colors.eventType.worker
  if (type.startsWith('hiw.')) return colors.eventType.hiw
  if (type.startsWith('timer.')) return colors.eventType.timer
  return colors.eventType.default
}

// ── Event summary text ──

export function summaryText(evt: EventPayload): string {
  const p = evt.payload
  if (!p) return ''
  if (p.text) {
    if (evt.type === 'worker.input') return 'text=...'
    return typeof p.text === 'string' ? ellipsis(p.text, 120) : ''
  }
  if (p.result)
    return typeof p.result === 'string' ? ellipsis(p.result, 120) : ''
  if (p.name) {
    return String(p.name) + (p.status ? ` [${p.status}]` : '')
  }
  if (p.error) return `error: ${ellipsis(String(p.error), 80)}`
  if (p.summary) return ellipsis(p.summary, 120)
  // Fallback: show first 2 non-empty fields, replace content with ...
  const parts: string[] = []
  if ('content' in p) {
    parts.push('content=...')
  }
  for (const [k, v] of Object.entries(p)) {
    if (k === 'worker_id' || k === 'call_id' || k === 'content') continue
    const vs = typeof v === 'string' ? v : JSON.stringify(v)
    if (vs && vs.length > 0 && vs !== '{}' && vs !== '[]') {
      parts.push(`${k}=${ellipsis(vs, 60)}`)
      if (parts.length >= 2) break
    }
  }
  return parts.join(', ')
}

// ── Worker type → color (for the worker-type tag) ──

export function getWorkerTypeColor(type: string, colors: import('../theme').Palette): string {
  switch (type) {
    case "reason":
      return colors.eventType.reason
    case "timer":
      return colors.eventType.timer
    case "hiw":
      return colors.eventType.hiw
    case "workspace":
      return colors.eventType.tool
    case "host":
      return colors.eventType.worker
    case "program":
      return colors.eventType.worker
    default:
      return colors.eventType.default
  }
}

// ── Target summary (truncate to 14 chars) ──

export function targetSummary(s: string): string {
  if (s.length <= 14) return s
  return s.slice(0, 14) + '\u2026'
}

// ── Truncation ──

function ellipsis(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n) + '…'
}

// oneLine collapses whitespace/newlines into a single line and ellipsizes it.
export function oneLine(s: string, n: number): string {
  const flat = s.replace(/\s+/g, ' ').trim()
  return ellipsis(flat, n)
}

// flatten collapses whitespace/newlines into a single line without truncating.
// Use with CSS text-overflow:ellipsis for a trailing ellipsis at the edge.
export { ellipsis }

// ── Context usage meta (reason.response) ──

// UsageMeta is the context usage a reason worker reports on its response, taken
// from the meta.usage payload it attaches to reason.response. Absent when the
// provider reported no usage (or the field is malformed).
export interface UsageMeta {
  input: number
  output: number
  total: number
  cacheRead: number
  contextWindow: number
  // contextPercent is 0..100 of total/contextWindow, or 0 when unknown.
  contextPercent: number
}

// getUsageMeta extracts the context usage carried in a reason.response event's
// meta payload. Returns undefined when the response carries none.
export function getUsageMeta(evt: EventPayload): UsageMeta | undefined {
  const meta = evt.payload?.meta
  if (!meta || typeof meta !== 'object') return undefined
  const u = (meta as Record<string, any>).usage
  if (!u || typeof u !== 'object') return undefined
  const input = Number(u.input_tokens) || 0
  const output = Number(u.output_tokens) || 0
  const total = Number(u.total_tokens) || (input + output)
  const cacheRead = Number(u.cache_read_tokens) || 0
  const contextWindow = Number(u.context_window) || 0
  const contextPercent = contextWindow > 0
    ? Math.round((total / contextWindow) * 100)
    : 0
  return { input, output, total, cacheRead, contextWindow, contextPercent }
}

// formatCompact abbreviates a token count for display as thousands: 1000+
// becomes "NK" rounded to the nearest thousand (128000 → "128K", 1200000 →
// "1200K", 1290 → "1K"). Values below 1000 stay raw.
export function formatCompact(n: number): string {
  if (n < 1000) return String(n)
  return Math.round(n / 1000) + 'K'
}

// UsageParts is the usage label split ready for rendering with a drawn
// vertical bar: left = context-window size + occupancy percent, right = this
// round's input/output token breakdown. Either side may be empty.
export interface UsageParts {
  left: string
  right: string
}

// getUsageParts renders the context usage of a response as i18n-localized text
// split into the two sides of a vertical separator (window/percent | in/out).
// Returns empty parts when the response carries no usage.
export function getUsageParts(evt: EventPayload, t: (key: any, vars?: Record<string, string | number>) => string): UsageParts {
  const m = getUsageMeta(evt)
  if (!m) return { left: '', right: '' }
  const left = (m.contextPercent > 0 && m.contextWindow > 0)
    ? t('talk.usage.window', { window: formatCompact(m.contextWindow), pct: m.contextPercent })
    : ''
  const right = [
    t('talk.usage.in', { n: formatCompact(m.input) }),
    t('talk.usage.out', { n: formatCompact(m.output) }),
  ]
  if (m.cacheRead) right.push(t('talk.usage.cache', { n: formatCompact(m.cacheRead) }))
  return { left, right: right.join(' · ') }
}

// ── Find referenced input event by trace_id ──

/**
 * Given a response event, finds the worker.input event that shares the same
 * trace_id, if any. Returns the input text, worker_id and event id, or null.
 */
export function findReferencedInput(events: EventPayload[], responseEvt: EventPayload): { text: string; workerId: string; evtId: string } | null {
  if (!responseEvt.trace_id) return null
  for (const evt of events) {
    if (evt.type === 'worker.input' && evt.trace_id === responseEvt.trace_id) {
      // Quote the message text only — attachment blocks render as chips.
      const text = parseAttachments(getInputText(evt)).text
      if (text) return { text, workerId: evt.worker_id, evtId: evt.id }
    }
  }
  return null
}

// ── Time formatting ──

export function formatTime(ts: number): string {
  return new Date(ts * 1000).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  })
}
