export interface EventPayload {
  id: string
  type: string
  worker_id: string
  target_worker_id: string
  timestamp: number
  trace_id: string
  request_id?: string
  recipients?: string[]
  payload: Record<string, any>
}

// WatchEntry is one capability a worker declares it responds to in its
// worker.ready "watch": the event type (its identity) plus an optional
// parameter schema (JSON Schema object form) and a description.
export interface WatchEntry {
  event: string
  desc?: string
  parameters?: Record<string, any>
}

export interface EventPattern {
  type: string
  source?: string
}

// PublishPattern is a worker's publish grant: an event type, optionally
// restricted to a directed target worker.
export interface PublishPattern {
  type: string
  target?: string
}

export interface WorkerInfo {
  id: string
  type: string
  credential?: string
  publish_allow?: PublishPattern[]
  subscribe_allow?: EventPattern[]
  online?: boolean
  // managed = an in-process worker the host supervises ("协程托管").
  // unmanaged = an OS process the swarm launched and supervises ("子进程").
  // Neither = a third-party worker that connected on its own ("三方 worker").
  managed?: boolean
  // managed: "running" | "suspended" | "stopped" — stopped means declared in
  // project.json but not instantiated; start spawns it via the host worker.
  state?: string
  unmanaged?: boolean
  unmanaged_state?: string // "running" | "stopped" (unmanaged only)
  // remote = a third-party worker that connected on its own: the project
  // issued its credential but does not launch any process, so there is no
  // unmanaged start/stop. Shown for the connect configuration to copy.
  remote?: boolean
  // Display metadata from the worker declaration: tags form a slash-path
  // hierarchy used to group workers in the target picker, and description
  // is a short purpose note shown as a dimmed second line.
  tags?: string[]
  description?: string
  // Declared construction params (project.json `params`), shown in the Info tab.
  params?: Record<string, unknown>
}

// The set of worker types that can be a talk conversation partner: ones that
// consume worker.input. Beyond the reason worker (which reasons), the coding
// reason worker is a reason-family worker that also consumes worker.input and
// publishes reason.response; a NIW (niq interface worker) accepts worker.input
// and bridges it to another niq instance; on the far side such a link is
// registered as a remote-niw worker (the A-side niw seen from B), which is
// likewise a valid talk partner. This is the single gate the mention component
// and the talk worker selector use.
export const TALK_PARTNER_TYPES = ['reason', 'coding', 'niw', 'remote-niw'] as const

// The reason FAMILY: workers built on the shared reasoning mechanism. They all
// emit the reason.* event stream and produce left-side conversation bubbles, so
// talk-view layout (avatars, alignment, direction) must treat them alike.
export const REASON_FAMILY_TYPES = ['reason', 'coding'] as const

// isReasonFamilyType reports whether a worker type is a reasoning worker.
export function isReasonFamilyType(type: string): boolean {
  return (REASON_FAMILY_TYPES as readonly string[]).includes(type)
}

// isTalkPartnerType reports whether a worker type can be a talk partner.
export function isTalkPartnerType(type: string): boolean {
  return (TALK_PARTNER_TYPES as readonly string[]).includes(type)
}

// RESPONSE_ONLY_TYPES is the whitelist the talk view's response-only mode keeps:
// the events that are visible when intermediate process (thinking, reasoning
// interruptions, tool invocations, request.* lifecycle) is hidden. It is sent to
// the backend as the ?type= whitelist so the stream AND history are filtered
// server-side — `events` is then exactly what renders, and the scroll/load-more
// math needs no extra client pass over the full set.
export const RESPONSE_ONLY_TYPES = [
  'worker.input',
  'worker.abort',
  'timer.timeout',
  'reason.response',
  'approval.request',
] as const

// CreateWorkerResult is what POST /api/workers/create returns.
export interface CreateWorkerResult {
  id: string
  type: string
  managed: boolean
  remote?: boolean
  credential?: string
}

// ProviderOption is one selectable LLM provider of a reason worker, as
// reported by the worker itself via the provider.list event.
export interface ProviderOption {
  name: string
  default: string
  models: string[]
}

// ProviderSelection is a provider/model pair — both the worker's current
// choice and the target of a switch.
export interface ProviderSelection {
  provider: string
  model: string
}

// ProviderListResult is what GET /api/workers/{id}/providers returns.
export interface ProviderListResult {
  providers: ProviderOption[]
  current: ProviderSelection
}

// ProviderSwitchResult is what POST /api/workers/{id}/provider returns.
export interface ProviderSwitchResult {
  done: boolean
  provider: string
  model: string
  error?: string
}

// ApprovalDecision is the approver's verdict on one approval request.
export interface ApprovalDecision {
  approved: boolean
  note?: string
  timestamp: number
}

// ApprovalEntry is one approval request tracked by the HIW (the default
// approver): the boundary-expansion a worker asked for and, once decided, the
// verdict. payload carries the request's full data so the UI can render any
// approval kind generically.
export interface ApprovalEntry {
  id: string
  request_id: string
  worker_id: string
  action?: string
  tool?: string
  path?: string
  trace_id?: string
  timestamp: number
  payload?: Record<string, any>
  decision?: ApprovalDecision | null
}

export interface ApprovalListResult {
  approvals: ApprovalEntry[]
}

export type ViewMode = 'talk' | 'events' | 'approvals' | 'workers' | 'mcp' | 'programs'

// McpServerInfo is one remote MCP server as mcp.server.list reports it. An mcp
// worker is an ordinary worker; a single one can front several servers.
export interface McpServerInfo {
  name: string
  url: string
  protocol?: string
  // "connected" (a live session) | "configured" (not connected yet)
  status?: string
  // Cached tool count; agentic mode only fills it once the tool list is read.
  tools?: number
}

// McpToolInfo is one tool as mcp.tool.list reports it: name, description and
// the JSON-schema of its arguments ("response" is the optional result schema).
export interface McpToolInfo {
  name: string
  description?: string
  schema?: unknown
  response?: unknown
}

// StagedAttachment is one attachment composed in the talk input. Images ride
// inline as base64; files are uploaded first and referenced by path. On send
// these become blocks of the structured worker.input "content" array.
export type StagedAttachment =
  | { id: string; kind: 'image'; name: string; mime: string; data: string; size: number }
  | { id: string; kind: 'file'; name: string; path: string; size: number }

// InputContent is one block of the structured worker.input "content" array —
// the llm-like shape HIW sends instead of the legacy <attachment> text
// envelope. Text, image (base64) and file (path on disk) are the kinds the
// reason input converter understands.
export type InputContent =
  | { type: 'text'; text: string }
  | { type: 'image'; name?: string; mime?: string; data: string; size?: number }
  | { type: 'file'; name?: string; path: string; size?: number }

// ViewSettings are the talk/events view preference toggles, persisted to
// localStorage across sessions.
export interface ViewSettings {
  thinkingExpanded: boolean
  compactMode: boolean
  streamingMode: boolean
  responseOnly: boolean
}
export type ViewSettingKey = keyof ViewSettings

// ContextInfo is what /api/context returns: which mode the SPA is in and, in
// project mode, which project it is attached to.
export interface ContextInfo {
  mode: 'control' | 'project'
  project?: string
  bus_port?: number
}

// ProjectInfo is a project's definition as exposed by the control-plane API.
export interface ProjectInfo {
  id: string
  created_at?: string
  ports?: { bus?: number; webui?: number }
  workers?: { type: string; id: string }[]
  running?: boolean
}

// ProgramInfo is a read-only summary of one program under the attached
// project, as served by /api/programs.
export interface ProgramInfo {
  name: string
  content_type?: string
  form_type?: string
  description?: string
  tags?: string[]
  locked?: boolean
  contents?: number
}

// ProgramDetail is the full content view of one program (metadata + entry
// body + sub-content paths), as served by GET /api/programs/{name}.
export interface ProgramDetail {
  name: string
  content_type?: string
  form_type?: string
  description?: string
  tags?: string[]
  locked?: boolean
  body: string
  contents?: string[]
}

// ProgramContent is one sub-content file's body, as served by
// GET /api/programs/{name}/content?path=….
export interface ProgramContent {
  name: string
  body: string
}

// ProjectStartResult is what {id}/start returns so the UI can redirect. The UI
// navigates to /p/<id>/ (see App.goToProjectWebui), so the ports are
// informational only.
export interface ProjectStartResult {
  project?: string
  webui_port?: number
  bus_port?: number
}

// WorkerDetailTab names one tab of the worker detail panel. Deep links (e.g.
// the talk badge's "Readme" menu entry) also use it to open a specific tab.
export type WorkerDetailTab = 'info' | 'readme' | 'events' | 'perms'
