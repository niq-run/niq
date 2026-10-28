// API service — all backend HTTP calls
import type {
  ApprovalListResult,
  ContextInfo,
  CreateWorkerResult,
  EventPayload,
  EventPattern,
  InputContent,
  ProgramContent,
  ProgramDetail,
  ProjectInfo,
  ProjectStartResult,
  ProgramInfo,
  ProviderListResult,
  ProviderSwitchResult,
  PublishPattern,
  WorkerInfo,
} from '../types'

// Control-plane base: the control plane owns the origin for the whole product —
// a project's WebUI is reached through it at /p/<id>/, never on the project's
// own port — so its API always lives at the origin root.
export const CONTROL = ''

// API_BASE is the base all project-resource calls (talk/workers, events, SSE,
// context) target. A project page is served at /p/<id>/ and must talk to
// /p/<id>/api/… so the control plane forwards it to that project; anywhere else
// (the control plane's own page, a project WebUI opened directly on its loopback
// port) stays same-origin at the root.
const pagePath = window.location.pathname.replace(/\/+$/, '')
let API_BASE = /^\/p\/[^/]+$/.test(pagePath) ? pagePath : ''
export function getApiBase(): string { return API_BASE }
function p(path: string): string { return API_BASE + path }

// fetchContext reports the SPA mode (control or project) and the control URL.
export async function fetchContext(): Promise<ContextInfo> {
  const res = await fetch(p('/api/context'))
  return res.json()
}

// fetchProjects lists projects from the control plane.
export async function fetchProjects(): Promise<ProjectInfo[]> {
  const res = await fetch(CONTROL + '/api/projects')
  return res.json()
}

// fetchTemplates lists project template names for the new-project dropdown.
export async function fetchTemplates(): Promise<string[]> {
  const res = await fetch(CONTROL + '/api/templates')
  return res.json()
}

// fetchTemplate returns a template's parsed JSON content.
export async function fetchTemplate(name: string): Promise<any> {
  const res = await fetch(CONTROL + `/api/templates/${encodeURIComponent(name)}`)
  if (!res.ok) throw new Error('fetch template failed: ' + res.status)
  return res.json()
}

// createTemplate clones an existing template into a new on-disk one.
export async function createTemplate(id: string, copyFrom: string): Promise<void> {
  const res = await fetch(CONTROL + '/api/templates', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, copy_from: copyFrom }),
  })
  if (!res.ok) throw new Error('create template failed: ' + res.status)
}

// fetchTemplatePreview returns the template a project would export (its worker
// configurations re-expressed) WITHOUT creating anything — the UI shows it as
// an editable draft. includeProgram additionally carries the reason worker's
// programs param (the programs/ resources are copied when the draft is saved).
export async function fetchTemplatePreview(projectId: string, includeProgram = false): Promise<any> {
  const q = includeProgram ? '?include_program=1' : ''
  const res = await fetch(CONTROL + `/api/projects/${encodeURIComponent(projectId)}/template-preview` + q)
  if (!res.ok) throw new Error('template preview failed: ' + res.status)
  return res.json()
}

// createTemplateBody creates a new on-disk template from a full (edited)
// template body. When the draft was exported from a project with the
// include-program flag, the save also copies the project's programs/ resources
// into the new template directory.
export async function createTemplateBody(
  id: string,
  template: any,
  opts?: { fromProject?: string; includeProgram?: boolean },
): Promise<void> {
  const body: Record<string, unknown> = { id, template }
  if (opts?.fromProject) {
    body.from_project = opts.fromProject
    body.include_program = !!opts.includeProgram
  }
  const res = await fetch(CONTROL + '/api/templates', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error(await errText(res, 'create template failed'))
}

// updateTemplate overwrites an existing template with an edited body.
export async function updateTemplate(name: string, template: any): Promise<void> {
  const res = await fetch(CONTROL + `/api/templates/${encodeURIComponent(name)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(template),
  })
  if (!res.ok) throw new Error(await errText(res, 'update template failed'))
}

// importTemplate imports a shared template package (.zip) into the templates
// dir. id may be empty — the backend derives it from the zip's folder name.
export async function importTemplate(id: string, file: File): Promise<void> {
  const form = new FormData()
  if (id) form.append('id', id)
  form.append('file', file)
  const res = await fetch(CONTROL + '/api/templates/import', {
    method: 'POST',
    body: form,
  })
  if (!res.ok) throw new Error(await errText(res, 'import template failed'))
}

// errText extracts the server's error message (the handlers put the reason in
// the body), falling back to the status line.
async function errText(res: Response, fallback: string): Promise<string> {
  let body = ''
  try { body = (await res.text()).trim() } catch { /* empty body */ }
  return body ? `${fallback}: ${body}` : `${fallback}: ${res.status}`
}

// fetchProviders returns the provider config (provider.json) verbatim.
export async function fetchProviders(): Promise<any> {
  const res = await fetch(CONTROL + '/api/providers')
  if (!res.ok) throw new Error('fetch providers failed: ' + res.status)
  return res.json()
}

// updateProviders replaces the provider config with an edited body.
export async function updateProviders(cfg: any): Promise<void> {
  const res = await fetch(CONTROL + '/api/providers', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(cfg),
  })
  if (!res.ok) throw new Error(await errText(res, 'update providers failed'))
}

// deleteTemplate removes an on-disk template.
export async function deleteTemplate(name: string): Promise<void> {
  const res = await fetch(CONTROL + `/api/templates/${encodeURIComponent(name)}`, { method: 'DELETE' })
  if (!res.ok) throw new Error('delete template failed: ' + res.status)
}

// createProject creates a project from a named template and starts it, returning
// the redirect URL of the new project's WebUI.
export async function createProject(id: string, template: string): Promise<ProjectStartResult> {
  const res = await fetch(CONTROL + '/api/projects', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id, template }),
  })
  if (!res.ok) throw new Error('create failed: ' + res.status)
  return res.json()
}

// linkProject opens an external directory as a project by symlinking it into
// the niq root. The path must be an absolute directory on the control plane's
// machine; the linked project's definition is returned. The directory may
// already be a niq project and resumes as-is.
export async function linkProject(path: string): Promise<ProjectInfo> {
  const res = await fetch(CONTROL + '/api/projects/link', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  if (!res.ok) throw new Error(await errText(res, 'link failed'))
  return res.json()
}

// DirEntry is one subdirectory the server-side directory picker offers.
export interface DirEntry { name: string; path: string }

// fetchDirs lists the immediate subdirectories of a server-side absolute path
// (empty = the niq projects root), for the project-directory picker. A browser
// file input cannot expose a real absolute path, so the server browses the
// filesystem instead — this works from localhost and remote alike.
export async function fetchDirs(path?: string): Promise<DirEntry[]> {
  const q = path ? '?path=' + encodeURIComponent(path) : ''
  const res = await fetch(CONTROL + '/api/projects/dirs' + q)
  if (!res.ok) throw new Error('fetch dirs failed: ' + res.status)
  return res.json()
}

// startProject asks the control plane to launch a project; returns the redirect
// URL of the project's own WebUI.
export async function startProject(id: string): Promise<ProjectStartResult> {
  const res = await fetch(CONTROL + `/api/projects/${encodeURIComponent(id)}/start`, { method: 'POST' })
  if (!res.ok) throw new Error('start failed: ' + res.status)
  return res.json()
}

// fetchArchived returns the archived-worker ids for the attached project.
export async function fetchArchived(): Promise<string[]> {
  const res = await fetch(p('/api/archived'))
  return res.json()
}

// setArchived marks/unmarks a worker as archived (persisted in the project's
// worker definitions). Returns the updated archived set.
export async function setArchived(id: string, archived: boolean): Promise<string[]> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/archived`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ archived }),
  })
  if (!res.ok) throw new Error('archive failed: ' + res.status)
  return res.json()
}

// stopProject asks the control plane to gracefully stop a running project.
export async function stopProject(id: string): Promise<void> {
  const res = await fetch(CONTROL + `/api/projects/${encodeURIComponent(id)}/stop`, { method: 'POST' })
  if (!res.ok) throw new Error('stop failed: ' + res.status)
}

// restartProject asks the control plane to gracefully stop and relaunch a
// running project on its reused ports. It resolves only once the new instance
// is actually serving, so the caller can treat it as a ready project.
export async function restartProject(id: string): Promise<ProjectStartResult> {
  const res = await fetch(CONTROL + `/api/projects/${encodeURIComponent(id)}/restart`, { method: 'POST' })
  if (!res.ok) throw new Error('restart failed: ' + res.status)
  return res.json()
}

// sendInput posts a user message addressed to one or more workers. The bus
// delivers the same event to every target, so each addressed worker receives
// its own copy.
//
// `target` (the primary addressee) rides along for servers that predate
// multi-target input: they ignore `targets`, and without `target` they would
// treat the send as unaddressed and broadcast it to every worker.
export async function sendInput(content: InputContent[], targets: string[], inputMode: string): Promise<void> {
  const res = await fetch(p('/api/input'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ content, targets, target: targets[0] ?? '', input_mode: inputMode }),
  })
  // Throw so a refused send keeps the draft: the caller clears the input on
  // success, and a silently-swallowed failure would look like a sent message.
  if (!res.ok) throw new Error((await res.text()).trim() || 'send failed: ' + res.status)
}

// UploadedFile is what POST /api/upload returns: the file's absolute path in
// the project's upload directory, to reference from an input attachment.
export interface UploadedFile {
  path: string
  name: string
  size: number
}

// uploadFile uploads one file (multipart) into the project's upload dir.
export async function uploadFile(file: File): Promise<UploadedFile> {
  const form = new FormData()
  form.append('file', file)
  const res = await fetch(p('/api/upload'), { method: 'POST', body: form })
  if (!res.ok) throw new Error((await res.text()).trim() || 'upload failed: ' + res.status)
  return res.json()
}

export async function abortWorker(target: string): Promise<void> {
  await fetch(p('/api/abort'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ target }),
  })
}

export async function fetchWorkers(): Promise<WorkerInfo[]> {
  const res = await fetch(p('/api/workers'))
  return res.json()
}

export async function suspendWorker(id: string): Promise<void> {
  await fetch(p(`/api/workers/${encodeURIComponent(id)}/suspend`), { method: 'POST' })
}

export async function resumeWorker(id: string): Promise<void> {
  await fetch(p(`/api/workers/${encodeURIComponent(id)}/resume`), { method: 'POST' })
}

// startWorker / stopWorker / restartWorker control an external (unmanaged)
// worker process launched by the project supervisor — and, for a managed
// worker, a rebuild from its declaration. The server answers as soon as the
// supervisor/host accepted the request, but a rebuild that blocks mid-flight
// would otherwise leave the UI's button spinning forever; bound it so the
// caller always resolves and can report "requested, still in progress".
const LIFECYCLE_TIMEOUT_MS = 30000

async function workerLifecycleOp(id: string, op: 'start' | 'stop' | 'restart'): Promise<void> {
  let res: Response
  try {
    res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/${op}`), {
      method: 'POST',
      signal: AbortSignal.timeout(LIFECYCLE_TIMEOUT_MS),
    })
  } catch (e) {
    const name = (e as Error)?.name
    if (name === 'TimeoutError' || name === 'AbortError') {
      throw new Error(op + ' is taking longer than expected — check the worker\'s state')
    }
    throw e
  }
  if (!res.ok) throw new Error((await res.text()).trim() || op + ' failed: ' + res.status)
}

export async function startWorker(id: string): Promise<void> {
  await workerLifecycleOp(id, 'start')
}

export async function stopWorker(id: string): Promise<void> {
  await workerLifecycleOp(id, 'stop')
}

export async function restartWorker(id: string): Promise<void> {
  await workerLifecycleOp(id, 'restart')
}

// deleteWorker permanently removes a worker: stops it, revokes its identity,
// deletes its persisted state, and drops its project.json declaration
// (unmanaged workers).
export async function deleteWorker(id: string): Promise<void> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}`), { method: 'DELETE' })
  if (!res.ok) throw new Error((await res.text()).trim() || 'delete failed: ' + res.status)
}

// createWorker persists a worker declaration (project.WorkerConfig-shaped
// body) and launches it. External workers start before the call resolves; a
// managed worker is spawned via the host worker, so the call waits for the
// host's terminal reply.
export async function createWorker(body: Record<string, unknown>): Promise<CreateWorkerResult> {
  const res = await fetch(p('/api/workers/create'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'create failed: ' + res.status)
  return res.json()
}

// updateWorkerMeta updates a worker's display metadata (tags / description),
// persisted to its project.json declaration and reflected live in the UI (no
// restart). Tags are sent as an array (empty = clear), description always sent.
export async function updateWorkerMeta(id: string, tags: string[], description: string): Promise<void> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/meta`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ tags, description }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'update metadata failed: ' + res.status)
}
// updateWorkerParams replaces a worker's construction params in its project.json
// declaration. Params are build-time input: the change applies on the next
// restart, not live.
export async function updateWorkerParams(id: string, params: Record<string, unknown>): Promise<void> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/params`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ params }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'update params failed: ' + res.status)
}
// updateWorkerAllow edits a worker's allow lists on the bus registry. Either
// list may be omitted to keep its current value. SubscribeAllow patterns may
// carry the optional source restriction ({type, source_id}).
export async function updateWorkerAllow(
  id: string,
  allow: { publish_allow?: PublishPattern[]; subscribe_allow?: EventPattern[] },
): Promise<void> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/allow`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(allow),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'update allow failed: ' + res.status)
}

// sendWorkerEvent sends an event (from the worker's watch contract) to a
// worker as HIW, with a client-supplied top-level payload.
export async function sendWorkerEvent(
  id: string,
  type: string,
  payload: Record<string, unknown>,
): Promise<string> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/event`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ type, payload }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'send event failed: ' + res.status)
  // The server returns the request id so the UI can correlate + show the reply.
  const data = await res.json().catch(() => null)
  return data?.request_id ?? ''
}

// fetchWorkerProviders asks a reason worker for its selectable providers and
// its current choice. It is answered by the worker over the bus, so it can take
// several seconds when a provider's model-list endpoint is slow.
export async function fetchWorkerProviders(id: string, signal?: AbortSignal): Promise<ProviderListResult> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/providers`), { signal })
  if (!res.ok) throw new Error((await res.text()).trim() || 'providers failed: ' + res.status)
  return res.json()
}

// switchWorkerProvider asks a reason worker to change its active provider and
// model. Both are required — the worker rejects an empty model rather than
// silently falling back to a default.
export async function switchWorkerProvider(id: string, provider: string, model: string): Promise<ProviderSwitchResult> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/provider`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ provider, model }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'provider switch failed: ' + res.status)
  return res.json()
}

// MountListResult is what GET /api/workers/{id}/mounts returns: a workspace
// worker's mounted directories (the first is the primary mount).
export interface MountListResult {
  mounts: string[]
  primary?: string
}

// MountMutateResult is what POST .../mounts/{add|remove} returns; on success
// it carries the fresh mount snapshot so the UI can re-render at once.
export interface MountMutateResult {
  done: boolean
  path: string
  mounts?: string[]
  primary?: string
  error?: string
}

// fetchWorkerMounts asks a workspace worker for its mounted directories over
// the bus (mount.list), so it can take a moment.
export async function fetchWorkerMounts(id: string, signal?: AbortSignal): Promise<MountListResult> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/mounts`), { signal })
  if (!res.ok) throw new Error((await res.text()).trim() || 'mounts failed: ' + res.status)
  return res.json()
}

// mutateWorkerMount mounts (add) or unmounts (remove) a directory on a
// workspace worker (mount.add / mount.remove). add applies directly when this
// UI's HIW is the worker's approver — the default; otherwise it parks behind
// an approval and the call times out.
export async function mutateWorkerMount(id: string, action: 'add' | 'remove', path: string): Promise<MountMutateResult> {
  const res = await fetch(p(`/api/workers/${encodeURIComponent(id)}/mounts/${action}`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'mount update failed: ' + res.status)
  return res.json()
}

// fetchApprovals lists the approval entries the HIW tracks (pending first,
// then the decided history).
export async function fetchApprovals(): Promise<ApprovalListResult> {
  const res = await fetch(p('/api/approvals'))
  if (!res.ok) throw new Error('approvals failed: ' + res.status)
  return res.json()
}

// decideApproval forwards the human's verdict for one pending approval (by its
// approval.request event id) through HIW to the requesting worker.
export async function decideApproval(id: string, approved: boolean, note = ''): Promise<void> {
  const res = await fetch(p(`/api/approvals/${encodeURIComponent(id)}/decision`), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ approved, note }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'decision failed: ' + res.status)
}

// fetchPrograms lists the programs under the attached project.
export async function fetchPrograms(): Promise<ProgramInfo[]> {
  const res = await fetch(p('/api/programs'))
  if (!res.ok) throw new Error('fetch programs failed: ' + res.status)
  return res.json()
}

// fetchProgramDetail returns one program's full detail via the program worker
// (metadata + entry body + sub-content paths).
export async function fetchProgramDetail(name: string): Promise<ProgramDetail> {
  const res = await fetch(p(`/api/programs/${encodeURIComponent(name)}`))
  if (!res.ok) throw new Error((await res.text()).trim() || 'fetch program failed: ' + res.status)
  return res.json()
}

// fetchProgramContent returns one sub-content file's body via the program
// worker's load tool, given the file's full abstract path.
export async function fetchProgramContent(name: string, path: string): Promise<ProgramContent> {
  const res = await fetch(p(`/api/programs/${encodeURIComponent(name)}/content?path=${encodeURIComponent(path)}`))
  if (!res.ok) throw new Error((await res.text()).trim() || 'fetch content failed: ' + res.status)
  return res.json()
}

// updateProgram edits a program's metadata (content type / description / tags)
// and its entry body via the program worker (upsert + write).
export async function updateProgram(
  name: string,
  meta: { content_type?: string; description?: string; tags?: string[] },
  body: string,
): Promise<void> {
  const res = await fetch(p(`/api/programs/${encodeURIComponent(name)}`), {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...meta, body }),
  })
  if (!res.ok) throw new Error((await res.text()).trim() || 'update failed: ' + res.status)
}

// deleteProgram removes a program and its sub-contents via the program worker.
export async function deleteProgram(name: string): Promise<void> {
  const res = await fetch(p(`/api/programs/${encodeURIComponent(name)}`), { method: 'DELETE' })
  if (!res.ok) throw new Error((await res.text()).trim() || 'delete failed: ' + res.status)
}

// EventsBefore options mirror the backend query filter: envelope scope
// (workers/roles), trace/request correlation, a type whitelist (types), and the
// blacklist the talk view uses to keep "invisible" event families (worker.*
// lifecycle noise, delta partials) out of history so pagination counts real
// rows.
export interface EventsBeforeOpts {
  workers?: string[]
  roles?: string[]
  trace?: string
  request?: string
  types?: string[]
  exclude?: string[]
  excludePrefix?: string[]
  keep?: string[]
}

export async function loadEventsBefore(anchorId: string, limit = 50, opts: EventsBeforeOpts = {}): Promise<any[]> {
  const params = new URLSearchParams()
  params.set('limit', String(limit))
  const { workers = [], roles = [], trace = '', request = '', types = [], exclude = [], excludePrefix = [], keep = [] } = opts
  for (const w of workers) params.append('worker', w)
  for (const role of roles) params.append('role', role)
  if (trace) params.set('trace', trace)
  if (request) params.set('request', request)
  for (const t of types) params.append('type', t)
  for (const t of exclude) params.append('exclude', t)
  for (const p of excludePrefix) params.append('exclude_prefix', p)
  for (const t of keep) params.append('keep', t)
  const res = await fetch(p(`/api/events/before/${anchorId}?${params}`))
  return res.json()
}

// fetchEventsByRequest returns all persisted events carrying the given
// request_id — the invocation plus its request.* answer. It is a one-shot
// historical query, decoupled from the live event stream.
export async function fetchEventsByRequest(requestId: string): Promise<EventPayload[]> {
  const res = await fetch(p(`/api/events/by-request/${encodeURIComponent(requestId)}`))
  return res.json()
}

// AskResult is one answered worker event: the reply's value when the worker
// completed it, or the rejection message when it failed.
export interface AskResult {
  ok: boolean
  // result is the reply's value — a string stays text, an object reply IS the
  // payload, anything else rides payload.result (see event.ReplyValue).
  result?: unknown
  text?: string
  error?: string
}

// askWorker drives a worker over the bus the way a human does: publish an
// event it watches as HIW (sendWorkerEvent), then poll the by-request index
// until its request.completed / request.failed answer is persisted. This is the
// same path the worker detail's readme uses; no worker needs a bespoke HTTP
// endpoint for the UI to query it.
async function askWorker(
  id: string,
  type: string,
  payload: Record<string, unknown>,
  opts: { timeoutMs?: number; intervalMs?: number } = {},
): Promise<AskResult> {
  const reqId = await sendWorkerEvent(id, type, payload)
  if (!reqId) throw new Error('no request id for ' + type)
  const timeoutMs = opts.timeoutMs ?? 15000
  const intervalMs = opts.intervalMs ?? 250
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, intervalMs))
    const evts = await fetchEventsByRequest(reqId).catch(() => [] as EventPayload[])
    const reply = evts.find(
      (e) => e.type === 'request.completed' || e.type === 'request.failed' || e.type === 'request.rejected',
    )
    if (!reply) continue
    const body = reply.payload ?? {}
    if (reply.type === 'request.completed') {
      const result = 'result' in body ? body.result : body
      return { ok: true, result, text: typeof result === 'string' ? result : JSON.stringify(result) }
    }
    return { ok: false, error: String(body.error ?? body.reason ?? type + ' rejected') }
  }
  throw new Error(type + ' timed out after ' + Math.round(timeoutMs / 1000) + 's')
}

// ── MCP ──
//
// An mcp worker's whole surface is ordinary bus events (mcp.server.list /
// mcp.server.add / mcp.server.update / mcp.server.remove / mcp.tool.list /
// mcp.expose), so the UI manages it through the generic ask path above.
// Mutations connect to the remote server before replying, hence the longer
// timeout than a pure read.

export function mcpListServers(id: string): Promise<AskResult> {
  return askWorker(id, 'mcp.server.list', {})
}

export function mcpListTools(id: string, server: string): Promise<AskResult> {
  return askWorker(id, 'mcp.tool.list', { server })
}

export function mcpAddServer(
  id: string,
  body: { name: string; url: string; protocol?: string; headers?: Record<string, string> },
): Promise<AskResult> {
  return askWorker(id, 'mcp.server.add', body, { timeoutMs: 30000 })
}

export function mcpUpdateServer(
  id: string,
  body: { name: string; url?: string; protocol?: string; headers?: Record<string, string> },
): Promise<AskResult> {
  return askWorker(id, 'mcp.server.update', body, { timeoutMs: 30000 })
}

export function mcpRemoveServer(id: string, name: string): Promise<AskResult> {
  return askWorker(id, 'mcp.server.remove', { name }, { timeoutMs: 30000 })
}

// mcpReadExpose reads the live exposure mode; mcpSetExpose switches it.
export function mcpReadExpose(id: string): Promise<AskResult> {
  return askWorker(id, 'mcp.expose', {})
}

export function mcpSetExpose(id: string, mode: string): Promise<AskResult> {
  return askWorker(id, 'mcp.expose', { mode }, { timeoutMs: 30000 })
}