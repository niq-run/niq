// API service — all backend HTTP calls
import type { WorkerInfo } from '../types'

export async function sendInput(text: string, target: string, inputMode: string): Promise<void> {
  await fetch('/api/input', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ text, target, input_mode: inputMode }),
  })
}

export async function abortWorker(target: string): Promise<void> {
  await fetch('/api/abort', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ target }),
  })
}

export async function fetchWorkers(): Promise<WorkerInfo[]> {
  const res = await fetch('/api/workers')
  return res.json()
}

export async function suspendWorker(id: string): Promise<void> {
  await fetch(`/api/workers/${encodeURIComponent(id)}/suspend`, { method: 'POST' })
}

export async function resumeWorker(id: string): Promise<void> {
  await fetch(`/api/workers/${encodeURIComponent(id)}/resume`, { method: 'POST' })
}

export async function loadEventsBefore(anchorId: string, limit = 50, workers: string[] = [], trace = ''): Promise<any[]> {
  const params = new URLSearchParams()
  params.set('limit', String(limit))
  for (const w of workers) params.append('worker', w)
  if (trace) params.set('trace', trace)
  const res = await fetch(`/api/events/before/${anchorId}?${params}`)
  return res.json()
}