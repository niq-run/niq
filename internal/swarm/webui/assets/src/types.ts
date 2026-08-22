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
