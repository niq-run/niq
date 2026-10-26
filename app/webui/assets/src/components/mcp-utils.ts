import type { McpServerInfo } from '../types'

// Helpers shared by the MCP view and its create form. An mcp worker is an
// ordinary worker whose config lives in `params`, so these mirror what
// core/impl/workers/mcp does when it parses a declaration — including the
// legacy nested `params.mcp` form, with flat keys winning.

// DeclaredMcpServer is a server as it appears in the declaration, which (unlike
// mcp.server.list) carries its headers. Edits made against the declaration keep
// them; runtime management cannot, since the list never reports them.
export interface DeclaredMcpServer extends McpServerInfo {
  headers?: Record<string, string>
}

export interface DeclaredMcp {
  fixed: boolean
  expose: string
  servers: DeclaredMcpServer[]
  allowedTools: string[]
}

// mcpMergedParams flattens a declaration's mcp params: the legacy nested
// params.mcp object is merged underneath the flat keys (flat wins), the same
// rule mcp.ParseConfig applies worker-side.
function mcpMergedParams(params?: Record<string, unknown>): Record<string, unknown> {
  const p = params ?? {}
  const nested =
    p.mcp && typeof p.mcp === 'object' && !Array.isArray(p.mcp) ? (p.mcp as Record<string, unknown>) : {}
  return { ...nested, ...p }
}

// mcpDeclared reads an mcp worker's build-time params.
export function mcpDeclared(params?: Record<string, unknown>): DeclaredMcp {
  const merged = mcpMergedParams(params)
  return {
    fixed: merged.fixed === true,
    expose: typeof merged.expose === 'string' && merged.expose ? merged.expose : 'agentic',
    servers: mcpServersParam(merged),
    allowedTools: Array.isArray(merged.allowed_tools) ? (merged.allowed_tools as unknown[]).map(String) : [],
  }
}

// mcpServersParam normalizes a params object's `servers` array (already
// flattened by mcpMergedParams) into the editable shape.
export function mcpServersParam(params?: Record<string, unknown>): DeclaredMcpServer[] {
  const raw = (params ?? {}).servers
  if (!Array.isArray(raw)) return []
  return raw
    .filter((s): s is Record<string, unknown> => !!s && typeof s === 'object' && typeof (s as Record<string, unknown>).name === 'string')
    .map((s) => {
      const headers =
        s.headers && typeof s.headers === 'object' && !Array.isArray(s.headers)
          ? (s.headers as Record<string, string>)
          : undefined
      return {
        name: String(s.name),
        url: typeof s.url === 'string' ? s.url : '',
        protocol: typeof s.protocol === 'string' ? s.protocol : undefined,
        headers,
      }
    })
}

// mcpWriteParams returns the params map to persist back: the merged form, with
// the legacy `mcp` key dropped (its values are now flat). Everything else — e.g.
// a custom `readme` — is preserved.
export function mcpWriteParams(params?: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = { ...mcpMergedParams(params) }
  delete out.mcp
  return out
}

// isValidMcpURL checks the only transport the worker speaks today: a remote
// Streamable HTTP endpoint. Local (stdio) MCP servers are not supported yet.
export function isValidMcpURL(url: string): boolean {
  return /^https?:\/\/\S+/i.test(url.trim())
}

// parseHeaders turns KEY=VALUE lines into a header map; blank lines and
// #comments are skipped. Values may carry ${ENV_VAR} references, resolved
// worker-side against the project environment.
export function parseHeaders(text: string): Record<string, string> | undefined {
  const out: Record<string, string> = {}
  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('#')) continue
    const i = trimmed.indexOf('=')
    if (i <= 0) continue
    out[trimmed.slice(0, i).trim()] = trimmed.slice(i + 1).trim()
  }
  return Object.keys(out).length > 0 ? out : undefined
}

// formatHeaders renders a declared header map back into the KEY=VALUE editor
// form, so a declaration edit starts from what the worker actually has.
export function formatHeaders(headers?: Record<string, string>): string {
  if (!headers) return ''
  return Object.keys(headers)
    .sort()
    .map((k) => k + '=' + headers[k])
    .join('\n')
}

// splitList splits a comma-separated field into bare values.
export function splitList(text: string): string[] {
  return text.split(',').map((x) => x.trim()).filter(Boolean)
}
