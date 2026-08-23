export interface EventPayload {
  id: string
  type: string
  worker_id: string
  target_worker_id: string
  timestamp: number
  trace_id: string
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
  managed?: boolean
  state?: string // "running" | "suspended" (managed only)
}

export type ViewMode = 'talk' | 'events' | 'workers'

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
