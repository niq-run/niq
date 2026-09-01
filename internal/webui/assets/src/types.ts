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

export interface EventPattern {
  type: string
  source_id?: string
}

export interface WorkerInfo {
  id: string
  type: string
  credential?: string
  publish_allow?: string[]
  subscribe_allow?: EventPattern[]
  online?: boolean
  // managed = an in-process worker the host supervises ("协程托管").
  // unmanaged = an OS process the swarm launched and supervises ("子进程").
  // Neither = a third-party worker that connected on its own ("三方 worker").
  managed?: boolean
  state?: string // "running" | "suspended" (managed only)
  unmanaged?: boolean
  unmanaged_state?: string // "running" | "stopped" (unmanaged only)
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

export type ViewMode = 'talk' | 'events' | 'workers'

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

// ProjectStartResult is what {id}/start returns so the UI can redirect.
export interface ProjectStartResult {
  project?: string
  webui_url?: string
  webui_port?: number
  bus_port?: number
}
