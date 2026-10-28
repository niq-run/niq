import type { PublishRow, SubscribeRow } from './components/AllowEditors'

// AllowDefaults is the pair of allow lists a worker starts with when its
// declaration names neither: the create form pre-fills its permission module
// from the SAME table the backend applies, so what the form shows is what the
// worker will actually get.
export interface AllowDefaults {
  publish: PublishRow[]
  subscribe: SubscribeRow[]
}

export type WorkerCreateMode = 'managed' | 'external' | 'remote'

// MANAGED_DEFAULTS mirrors the per-type initial grants in
// niq/app/project/build.go (specConnect's publish list +
// subAllowFromParams's defaults). Keep this table in sync with those builders:
//
//   reason / coding  resolveReasonFamily       publish *  / allow.ready-gone-discover-abort-input
//   workspace        buildWorkspaceSpec        request.*, worker.ready, approval.request / worker.discover
//   program          buildProgramSpec          request.*, worker.ready, *  / worker.discover
//   niw              buildNIWSpec              worker.input, request.*, worker.ready / worker.input, worker.discover
//   mcp              buildMCPSpec              request.*, worker.ready / worker.discover, worker.ready, worker.gone
const MANAGED_DEFAULTS: Record<string, { publish: string[]; subscribe: string[] }> = {
  reason: {
    publish: ['*'],
    subscribe: ['worker.ready', 'worker.gone', 'worker.discover', 'worker.abort', 'worker.input'],
  },
  coding: {
    publish: ['*'],
    subscribe: ['worker.ready', 'worker.gone', 'worker.discover', 'worker.abort', 'worker.input'],
  },
  workspace: {
    publish: ['request.*', 'worker.ready', 'approval.request'],
    subscribe: ['worker.discover'],
  },
  program: {
    publish: ['request.*', 'worker.ready', '*'],
    subscribe: ['worker.discover'],
  },
  niw: {
    publish: ['worker.input', 'request.*', 'worker.ready'],
    subscribe: ['worker.input', 'worker.discover'],
  },
  mcp: {
    publish: ['request.*', 'worker.ready'],
    subscribe: ['worker.discover', 'worker.ready', 'worker.gone'],
  },
}

// NIW_FARSIDE_DEFAULTS is what a remote-niw identity gets on the bus it dials
// into (see buildRemoteIdentity in niq/app/project/external.go): the bridge's
// narrow grant rather than the "*" every other third-party worker gets.
const NIW_FARSIDE_DEFAULTS = {
  publish: ['worker.input', 'request.*', 'worker.ready'],
  subscribe: ['worker.input', 'worker.discover'],
}

// THIRD_PARTY_DEFAULTS is the fallback for a launched or self-connecting
// third-party worker of any other type (buildRemoteIdentity's non-niw branch).
const THIRD_PARTY_DEFAULTS = { publish: ['*'], subscribe: ['*'] }

function rows(d: { publish: string[]; subscribe: string[] }): AllowDefaults {
  return {
    // An unrestricted grant is spelled the way the backend stores it: publish
    // uses target "*", subscribe uses source "*" (an omitted source stays
    // accepted but is normalized to "*" on read).
    publish: d.publish.map((type) => ({ type, target: '' })),
    subscribe: d.subscribe.map((type) => ({ type, source: '*' })),
  }
}

// defaultAllow returns the initial grants the backend would give a worker of
// this kind and type. Unknown managed types yield empty lists (= the backend
// decides; nothing to show).
export function defaultAllow(mode: WorkerCreateMode, type: string): AllowDefaults {
  if (mode === 'managed') {
    const d = MANAGED_DEFAULTS[type]
    return d ? rows(d) : { publish: [], subscribe: [] }
  }
  // Both external (host-launched) and remote (self-connecting) third-party
  // workers share one identity builder, so their defaults are the same.
  return rows(type.trim() === 'remote-niw' ? NIW_FARSIDE_DEFAULTS : THIRD_PARTY_DEFAULTS)
}

// samePublish / sameSubscribe compare two row lists. The create form uses them
// to tell an untouched (still default) draft from one the user has edited, so
// switching type only re-seeds the rows that were never customized.
export function samePublish(a: PublishRow[], b: PublishRow[]): boolean {
  return a.length === b.length && a.every((r, i) => r.type === b[i].type && r.target === b[i].target)
}

export function sameSubscribe(a: SubscribeRow[], b: SubscribeRow[]): boolean {
  return a.length === b.length && a.every((r, i) => r.type === b[i].type && r.source === b[i].source)
}
