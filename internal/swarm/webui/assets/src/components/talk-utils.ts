// ── Utility functions for TalkView / EventRow ──

import type { EventPayload } from '../types'

// ── Content extraction ──

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

// ── Type checks ──

export function isToolEvent(type: string): boolean {
  return type === 'tool.requested' || type === 'tool.completed' || type === 'tool.failed' || type === 'tool.rejected'
}

export function isReasonBoundary(type: string): boolean {
  return type === 'reason.start' || type === 'reason.end'
}

// ── Tool helpers ──

export function toolSummary(evt: EventPayload): string {
  const name = evt.payload?.name as string || ''
  const err = evt.type === 'tool.failed' ? (evt.payload?.error as string || '') : ''
  return `[${name}]${err ? ' — ' + truncate(err, 60) : ''}`
}

export function toolCallId(evt: EventPayload): string {
  return (evt.payload?.call_id as string) || ''
}

export function toolContent(evt: EventPayload, formatted: boolean): string {
  const format = (v: any): string =>
    formatted ? JSON.stringify(v, null, 2) : JSON.stringify(v)
  if (evt.type === 'tool.requested') {
    const args = evt.payload?.arguments
    if (args) return format(args)
    return ''
  }
  if (evt.type === 'tool.completed') {
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
  if (evt.type === 'tool.failed') {
    const err = evt.payload?.error
    if (err) return String(err)
    return ''
  }
  if (evt.type === 'tool.rejected') {
    const reason = evt.payload?.reason
    if (reason) return String(reason)
    return ''
  }
  return ''
}

// ── Event type → color ──

export function getTypeColor(type: string, colors: import('../theme').Palette): string {
  if (type.startsWith('tool.')) return colors.eventType.tool
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
    const args = p.arguments ? ' ' + JSON.stringify(p.arguments) : ''
    return (
      String(p.name) + ellipsis(args, 120) + (p.status ? ` [${p.status}]` : '')
    )
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

export { ellipsis }

export function truncate(s: string, n: number): string {
  return ellipsis(s, n)
}

// ── Find referenced input event by trace_id ──

/**
 * Given a response event, finds the worker.input event that shares the same
 * trace_id, if any. Returns the input text and worker_id, or null.
 */
export function findReferencedInput(events: EventPayload[], responseEvt: EventPayload): { text: string; workerId: string } | null {
  if (!responseEvt.trace_id) return null
  for (const evt of events) {
    if (evt.type === 'worker.input' && evt.trace_id === responseEvt.trace_id) {
      const text = getInputText(evt)
      if (text) return { text, workerId: evt.worker_id }
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

// ── Event payload formatting ──

export function formatEventPayload(evt: EventPayload): string {
  const p = evt.payload
  if (!p) return ''
  const parts: string[] = []
  if (p.text) parts.push(`text=${truncate(String(p.text), 80)}`)
  if (p.name) parts.push(`name=${p.name}`)
  if (p.result) parts.push(`result=${truncate(String(p.result), 60)}`)
  if (p.error) parts.push(`error=${truncate(String(p.error), 60)}`)
  if (p.summary) parts.push(`summary=${truncate(p.summary, 60)}`)
  if (parts.length === 0) {
    for (const [k, v] of Object.entries(p)) {
      if (k === 'worker_id' || k === 'call_id' || k === 'content') continue
      const vs = typeof v === 'string' ? v : JSON.stringify(v)
      if (vs && vs.length > 0 && vs !== '{}' && vs !== '[]') {
        parts.push(`${k}=${truncate(vs, 60)}`)
        if (parts.length >= 2) break
      }
    }
  }
  return parts.join(', ') || '(empty)'
}