# project/api — one project's data port and its bus half

`project/api` is the HTTP surface of a single project, plus the object that makes
the project visible on its own event bus. On the HTTP side it is a **data port**:
JSON and SSE for workers, events, approvals, uploads and programs, bound to a
loopback address by the assembly. It serves no SPA and performs no auth — the
gateway authenticates the operator and serves the bundle, then proxies here.

The other half is `iface.Backend`, implemented in `iface.go`. The app is the
backend of the project's interface worker, so one `*Server` serves HTTP to the
human and events to the bus: the worker faces the bus, the app faces the human.

## Where it sits

```
browser ──► gateway (auth, SPA) ──proxy──► api.Server  (127.0.0.1)
                                                 │
                                                 │ SetHandler
                                                 ▼
                                    iface.Worker (type interface)
                                                 │
                                                 ▼
                                            project event bus
```

The assembly constructs `api.New(...)` first, while it holds no bus handle; the
app then becomes the `iface.Backend` of the interface worker the assembly joins,
and `SetHandler` delivers the handle before the app binds and serves.

## Endpoint groups

Here `...` stands for `/api/workers/{id}`.

| group | routes |
|---|---|
| stream and history | `GET /api/stream` (SSE), `GET /api/events/before/{id}`, `GET /api/events/by-request/{requestId}` |
| input and abort | `POST /api/input`, `POST /api/abort` |
| uploads | `POST /api/upload` |
| worker read | `GET /api/workers`, `GET /api/archived` |
| worker ACL | `PUT .../allow` |
| worker lifecycle | `POST .../suspend`, `.../resume`, `.../start`, `.../stop`, `.../restart`, `DELETE /api/workers/{id}`, `POST .../archived` |
| worker declaration | `PUT .../meta`, `PUT .../params`, `POST /api/workers/create` |
| worker introspection | `GET`/`POST .../providers`, `.../provider`, `GET .../mounts`, `POST .../mounts/add`, `.../mounts/remove`, `POST .../event` |
| approvals | `GET /api/approvals`, `POST /api/approvals/{id}/decision` |
| programs | `GET /api/programs`, `{name}`, `{name}/content`, `PUT`, `DELETE` |
| context | `GET /api/context` |

The stream is a live tail: `FollowLive` advertises a watermark as the first
control event, heartbeats every 15 seconds, and history is paged separately
through `before`. The worker table merges registry identities (credential, ACL,
online) with the manifest catalog (label, placement, state), including declared
workers that have never started. Programs degrade to an empty list when the
program worker is absent; provider and mount calls report the worker offline.

## The bus half

`SetHandler` injects the worker's `iface.Handler`; `busHandler` returns an
actionable error while it is missing, so reads keep working and writes do not
silently no-op. Everything the human says goes through that one handle:
`sendInput` publishes `worker.input`, `sendEvent` publishes a declared event and
returns its request id, and `sendApprovalDecision` forwards a verdict through the
worker's `iface.Inbox`. Approvals are owned — and persisted — by the interface
worker; the server is a thin view.

Not every write is a bus event, and the split is deliberate:

| write | path |
|---|---|
| lifecycle and declaration (start/stop/restart, suspend/resume, destroy, archive, spawn, meta, params) | a request to the host worker, correlated by trace id |
| ACL (`PUT .../allow`) | a direct call to the bus identity control plane |
| human input, arbitrary events, approval decisions | the injected `iface.Handler` |
| reads (stream, history, worker table) | the app's own `EventLog`, registry and catalog |

## Files

| File | Responsibility |
|---|---|
| `server.go` | `Server`, `New` and the full route table, bind/serve, upload-directory resolution, SSE, every handler group, the `ContextInfo`/`WorkerView`/`ProgramSummary` shapes, and `cors`. |
| `iface.go` | The `iface.Backend` half: `SetHandler`, the human's writes (`sendInput`, `sendEvent`), and approval-inbox access (`inbox`, `approvals`, `sendApprovalDecision`). |
| `server_test.go` | Routing and IO: ephemeral bind, ACL through bus control, context mode, program list/parse, upload persistence, input target rules, worker-table merge, lifecycle via host, create/duplicate conflicts, params and meta. |
| `iface_test.go` | The bus half's failure modes: writes before a handler is injected, and a worker that does not serve approvals. |

## Invariants

- **Loopback, no auth, no SPA.** The caller binds a loopback address; the gateway
  owns authentication and the bundle.
- **Core only.** Nothing here imports another `app/standalone` package or knows
  `store`, `worker` or `launcher` types.
- **One write path for the human.** Every human write goes through the injected
  `iface.Handler`; a missing handler is an error, never a silent drop.
- **Lifecycle is the host's.** No handler here acts on a worker directly; it asks
  the host worker, which owns the declaration, the state and the lock.
- **ACL is not lifecycle.** Grants are written to the bus identity control plane,
  the one durable owner of who may say what.
- **Nothing is persisted here.** Approvals belong to the interface worker and
  programs to the program worker; the server holds no state of its own.
