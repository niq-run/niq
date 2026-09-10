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
}

// CreateWorkerResult is what POST /api/workers/create returns.
export interface CreateWorkerResult {
  id: string
  type: string
  managed: boolean
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

export type ViewMode = 'talk' | 'events' | 'approvals' | 'workers'

// StagedAttachment is one attachment composed in the talk input, appended to
// the input text as an <attachment> block on send. Images ride inline as
// base64; files are uploaded first and referenced by path.
export type StagedAttachment =
  | { id: string; kind: 'image'; name: string; mime: string; data: string; size: number }
  | { id: string; kind: 'file'; name: string; path: string; size: number }

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
// project (control_url) where to reach the control plane.
export interface ContextInfo {
  mode: 'control' | 'project'
  project?: string
  control_url?: string
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

// ProjectStartResult is what {id}/start returns so the UI can redirect.
export interface ProjectStartResult {
  project?: string
  webui_url?: string
  webui_port?: number
  bus_port?: number
}
