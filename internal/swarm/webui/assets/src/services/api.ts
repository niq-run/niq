// API service — all backend HTTP calls
import type { ContextInfo, ProjectInfo, ProjectStartResult, WorkerInfo } from '../types'

// Control-plane base: project management always talks to the control plane on
// 9527 (dev server reaches it via the vite proxy when no base is set, or
// directly cross-origin in a running project).
export const CONTROL = 'http://127.0.0.1:9527'

// API_BASE is the base all project-resource calls (talk/index, workers, events,
// SSE, context) target. Empty means same-origin (a served project webui, or the
// dev proxy toward the control/back end). The App sets it from ?project=&port=
// when developing against a specific project instance.
let API_BASE = ''
export function setApiBase(base: string) { API_BASE = base }
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

export async function sendInput(text: string, target: string, inputMode: string): Promise<void> {
  await fetch(p('/api/input'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text, target, input_mode: inputMode }),
  })
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

export async function loadEventsBefore(anchorId: string, limit = 50, workers: string[] = [], trace = ''): Promise<any[]> {
  const params = new URLSearchParams()
  params.set('limit', String(limit))
  for (const w of workers) params.append('worker', w)
  if (trace) params.set('trace', trace)
  const res = await fetch(p(`/api/events/before/${anchorId}?${params}`))
  return res.json()
}