// ── Utility functions for TalkView / EventRow ──

import type { EventPayload } from '../types'

// ── Input addressing (@mentions) ──

// resolveMentionId resolves a raw @token to a known worker id, dropping the
// sentence punctuation that rides along in prose: "@ops." is the worker ops
// followed by a period. Ids use [\w.-], so `.` is the only character that can
// be either id or punctuation; a token that resolves as-is is never trimmed.
export function resolveMentionId(raw: string, isKnown: (id: string) => boolean): string | null {
  let id = raw
  while (id !== '' && !isKnown(id) && id.endsWith('.')) id = id.slice(0, -1)
  return isKnown(id) ? id : null
}

// mentionsIn lists the workers @-mentioned in a message, in first-appearance
// order and de-duplicated. Unresolved/@-in-prose tokens do not count, and an @
// glued to a word (an email like a@b.c) is prose, not addressing.
export function mentionsIn(text: string, isKnown: (id: string) => boolean): string[] {
  const out: string[] = []
  for (const m of text.matchAll(/(?<![\w.-])@([\w.-]+)/g)) {
    const id = resolveMentionId(m[1], isKnown)
    if (id && !out.includes(id)) out.push(id)
  }
  return out
}

// mergeTargets unions two recipient lists, keeping first-seen order and
// dropping duplicates. Typing an @mention appends to the recipient set rather
// than replacing it, so a draft can add to the sidebar selection without
// silently dropping it.
export function mergeTargets(base: string[], extra: string[]): string[] {
  const out = [...base]
  for (const id of extra) {
    if (!out.includes(id)) out.push(id)
  }
  return out
}

// leadingMentions splits the addressing prefix off a message: the run of
// @-tokens at its head, plus the body that follows. This is structural — it
// takes any @token, without consulting the worker list — so a body can be
// recovered before the workers have loaded.
export function leadingMentions(text: string): { ids: string[]; rest: string } {
  const ids: string[] = []
  let rest = text
  for (;;) {
    const m = rest.match(/^\s*@([\w.-]+)(?:[ \t]+|$)/)
    if (!m) break
    ids.push(m[1])
    rest = rest.slice(m[0].length)
  }
  return { ids, rest }
}

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

// inputContent extracts a worker.input event's message body (text) and its
// attachments for rendering. A payload carrying a structured "content" array
// yields the joined text blocks plus the image/file blocks as attachments; a
// plain-text event (payload.text, no content array) yields that text with no
// attachments.
//
// Returns the joined plain text and the ordered attachment list; media blocks
// (image base64, file paths) are not text and are returned as attachments.
// The legacy <attachment> text envelope is no longer produced or parsed.
//
// Each block maps:
//	text  -> appended to text
//	image -> attachment {kind:'image', mime, data, name}
//	file  -> attachment {kind:'file', name, path, size}
//	other -> appended to text if it carries text
//
export function inputContent(evt: EventPayload): { text: string; attachments: ParsedAttachment[] } {
  const content = evt.payload?.content
  if (!Array.isArray(content)) {
    // Plain-text event: no structured content, no attachments.
    return { text: getInputText(evt), attachments: [] }
  }
  let text = ''
  const attachments: ParsedAttachment[] = []
  for (const c of content) {
    const blk = c as { type?: string; text?: string; name?: string; mime?: string; data?: string; path?: string; size?: number }
    switch (blk?.type) {
      case 'text':
        if (blk.text) text += (text ? '\n' : '') + blk.text
        break
      case 'image':
        if (blk.data) attachments.push({ kind: 'image', mime: blk.mime || 'image/png', data: blk.data.replace(/\s+/g, ''), name: blk.name })
        break
      case 'file':
        if (blk.path) attachments.push({ kind: 'file', name: blk.name || blk.path, path: blk.path, size: Number(blk.size) || 0 })
        break
      default:
        // Unknown block type: fall through to text if it carried any.
        if (blk?.text) text += (text ? '\n' : '') + blk.text
    }
  }
  return { text, attachments }
}

// getContentText returns the text content of an event, for events that carry a
// content array that is not the input envelope (e.g. reason responses).
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

// ── Renderable input content ──

// ParsedAttachment is one media block of a structured input's "content" array:
// an image carried inline as base64, or a file referenced by path on disk.
export interface ParsedAttachment {
  kind: 'image' | 'file'
  mime?: string
  data?: string // image: base64
  name?: string
  path?: string // file
  size?: number
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

    // No "result" wrapper: the payload IS the reply (the bus rule for object
    // replies). program.run answers { output, log? }, workspace.readImg answers
    // { content: [...] } — without this fallback the talk view showed the
    // arguments and an empty result.
    const payload = evt.payload
    if (!payload || Object.keys(payload).length === 0) return ''
    if (Array.isArray(payload.content)) return formatContentBlocks(payload.content, formatted)
    return format(payload)
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

// formatContentBlocks renders a multi-modal reply without dumping base64: text
// blocks are joined, other blocks are summarized (the full payload is still in
// the event log).
function formatContentBlocks(blocks: any[], formatted: boolean): string {
  const parts: string[] = []
  for (const b of blocks) {
    const blk = b as { type?: string; text?: string; mime?: string; data?: string; name?: string; path?: string }
    if (blk?.type === 'text' && typeof blk.text === 'string') {
      parts.push(blk.text)
    } else if (blk?.type === 'image') {
      const bytes = typeof blk.data === 'string' ? Math.floor((blk.data.length * 3) / 4) : 0
      parts.push(`[image ${blk.mime || 'image'}${bytes ? `, ~${bytes} bytes` : ''}]`)
    } else if (blk?.type === 'file') {
      parts.push(`[file ${blk.name || blk.path || ''}]`.trim())
    } else {
      parts.push(JSON.stringify(blk))
    }
  }
  return parts.join(formatted ? '\n\n' : '\n')
}

// ── Event type → color ──
//
// Colour encodes outcome, not family, so the stream can be scanned for what
// finished and what broke:
//
//   reason.*            → purple  (checked first: reasoning stays purple even
//                                  when it is interrupted)
//   *.failed / .rejected / .error → red
//   *.completed / .done / .succeeded → green
//   request.* / tool.* / *.request / *.call → yellow  (a call in flight)
//   everything else     → grey    (lifecycle / infrastructure)
//
// Failure and completion match the suffix, so they also cover worker-specific
// families (e.g. mcp.tool.call.failed), and are tested before the request rule
// so request.completed / request.failed land in green / red, not yellow.

const FAILED_SUFFIXES = ['.failed', '.rejected', '.error', '.errored']
const COMPLETED_SUFFIXES = ['.completed', '.done', '.succeeded', '.success', '.finished']

function endsWithAny(type: string, suffixes: string[]): boolean {
  return suffixes.some(s => type.endsWith(s))
}

export function getTypeColor(type: string, colors: import('../theme').Palette): string {
  if (type.startsWith('reason.')) return colors.eventType.reason
  if (endsWithAny(type, FAILED_SUFFIXES)) return colors.eventType.failed
  if (endsWithAny(type, COMPLETED_SUFFIXES)) return colors.eventType.completed
  if (
    type.startsWith('request.') ||
    type.startsWith('tool.') ||
    type.endsWith('.request') ||
    type.endsWith('.call')
  ) return colors.eventType.request
  return colors.eventType.other
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
//
// Four groups, not a colour per type: every built-in worker shares one tone,
// reason and coding each get their own, and every external (third-party) worker
// shares one. The tag's text already names the type, so colour only has to
// separate the groups — which keeps the palette calm.

const BUILTIN_TYPES = new Set([
  'host',
  'workspace',
  'timer',
  'mcp',
  'directory',
  'history',
  'program',
  'niw',
  'remote-niw',
  'hiw',
])

export function getWorkerTypeColor(type: string, colors: import('../theme').Palette): string {
  if (type === 'reason') return colors.typeTag.reason
  if (type === 'coding') return colors.typeTag.coding
  if (BUILTIN_TYPES.has(type)) return colors.typeTag.builtin
  return colors.typeTag.external
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
 * Given a response event, finds the worker.input event it is answering and
 * return its text, worker id and event id, or null.
 *
 * A linked/broadcast exchange shares ONE trace_id across many worker.input
 * events, so we can't just pick the first match (that would quote the oldest
 * message in the trace). Instead walk the timeline in order and keep the LAST
 * worker.input at-or-before this response — i.e. the specific message directly
 * before it. The array is chronologically sorted, so stopping when we reach the
 * response's own row guarantees we only look at inputs that precede it (and
 * stays correct even when timestamps tie).
 */
export function findReferencedInput(events: EventPayload[], responseEvt: EventPayload): { text: string; workerId: string; evtId: string } | null {
  if (!responseEvt.trace_id) return null
  let ref: { text: string; workerId: string; evtId: string } | null = null
  for (const evt of events) {
    if (evt.id === responseEvt.id) break // passed this response's timeline position
    if (evt.type !== 'worker.input' || evt.trace_id !== responseEvt.trace_id) continue
    // Quote the message text only — media blocks (images/files) render as chips.
    const text = inputContent(evt).text
    if (text) ref = { text, workerId: evt.worker_id, evtId: evt.id }
  }
  return ref
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
