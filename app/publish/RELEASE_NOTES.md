# niq v0.16.0

A release about the shape of the thing. Almost nothing here is a new capability:
it is the worker model made explicit — one lifecycle engine, one storage boundary
per owner, one reusable "interface worker" behind which every outward-facing
surface sits — and then two real instances talking to each other, which is how
the transport bugs at the end were found.

The sentence the whole release orbits: **a worker type names what is behind it,
and the layer that can persist something is the layer that owns writing it.**

## Highlights

- **A worker is a declaration, a placement and a backend.** One lifecycle backend
  for every placement, one `params` bag as the only construction input, and
  worker storage split into `project.json` (metadata), `workers.json` (active
  declarations), `workers.archived.json` (archived) and `workers/<id>/` (runtime
  state). `id/identities.json` is gone; the runtime identity table is a
  projection of the declarations.
- **`iface`, the interface worker.** A reusable base for any worker whose
  capability is reaching outward — a UI, an API app, a chat bridge, another niq.
  `webui` is now one of them, and the TypeScript SDK ships the same contract.
- **`niw` is now `niq`**, rebuilt on that base: a worker whose backend is another
  niq instance. `hiw` is gone; the WebUI is an app-hosted interface worker.
- **Edits have owners.** Declaration edits are the host's `update` tool, ACL
  edits are `acl.update` (identity data, not a lifecycle verb), and archive is
  one operation instead of four steps in an HTTP handler.
- **Approvals are a capability of the interface worker**, opt-in, with a
  snapshot for recovery — and the same inbox a terminal or a menubar notifier
  will use.
- **Spawn parameters are checked per type** instead of being silently dropped.

## 1. One lifecycle backend, one params bag

Before this release a worker's definition lived in several typed fields
(`instruction`, `provider`, `model`, `mounts`, `approver`, …) next to a `params`
map, and it was never clear which one won. Now there is exactly one:
**`params` is the only construction input**, `type`/`id`/`placement`/`command`/
`cwd` are fixed fields, and the rest is the type's business. Each type declares
which parameters it reads, and a spawn naming something else fails with the list
of what it does accept rather than quietly ignoring it.

`host` drives one `worker.Backend` for all three placements:

| Placement | Lifecycle owner |
|---|---|
| `in-process` | the host's lifecycle engine (snapshot / restore) |
| `managed-process` | the process supervisor (launch, backoff, restart) |
| `unmanaged-process` | nobody here — the third party starts itself |

Concurrency has a contract now, not a hope: lifecycle operations are serialized
per worker id, slow calls (`Connect`/`Build`/`Start`/`Snapshot`/`Stop`, store I/O)
run outside the registry lock, snapshots carry a sequence number so a slow one
cannot overwrite a newer one, and destroy stops first and only then drops the
entry — a failed stop cannot make a live worker invisible.

## 2. Storage: three boundaries, one owner each

```
project.json             metadata only
root_secret              bus token signing key
workers.json             active declarations
workers.archived.json    archived declarations (same shape)
workers/<id>/            runtime state: state.json, stdout.log, process state
```

- **Declarations** are owned by the declaration store. Every write goes through
  one lock, and the archive move writes the destination before the source, so a
  crash can only duplicate a declaration, never lose one.
- **Runtime state** is owned by the worker store (`state.json`) and by the process
  supervisor (process log + child state dir). Each deletes only what it created;
  the declaration store no longer deletes runtime state at all.
- **Identity** has no file. `workers.json` is the durable source, the registry is
  its runtime projection, and `root_secret` is the only signing authority.

## 3. `iface`: the interface worker

A worker whose capability is reaching outward, not a domain of its own. It faces
the bus like every worker (identity, ACL, presence, discover, tools) and holds
injected **backends** behind it — the same shape a workspace worker has with its
filesystem backend.

```go
type Backend interface{ SetHandler(h Handler) }

type Handler interface {
    ID() string
    Send(ctx, evt, targets ...string) error
    Broadcast(ctx, evt) error
    Request(ctx, target string, evt event.Event) (event.Event, error)
    OnEvent(fn func(event.Event), types ...event.EventType) Subscription
}
```

- `SetHandler` runs **once, before `Start`**, so a backend builds its outward side
  already knowing how to reach in.
- `Request` owns the correlation that every caller used to hand-roll: ids, the
  wait, the timeout, and `request.failed`/`request.rejected` as errors.
- Inbound fan-out is per backend, each with its own queue and goroutine: a slow or
  panicking backend cannot stall the loop or starve another.
- Optional capabilities are assertions on the same handle: `Runnable` (a backend
  with its own lifetime), `Stateful` (its durable bytes), `Inbox` (approvals).

It is deliberately **not** a bus control plane: editing ACLs and rendering the
global event stream belong to the app's own bus handles. Routing them through an
interface worker would add a hop and hide what they are.

## 4. `niq`, and the end of `hiw`

`niw` was 733 lines of hand-rolled bus plumbing for one idea: link this bus to
another niq. It is now `niq` — a worker whose backend *is* another niq instance —
built on `iface`:

```
iface.Worker   the bus half: loop, tool dispatch, presence, Handler
niq.Backend    the far side: dial + backoff + reconnect, the two recipient
               bindings, niq.* tools, Snapshot/Restore
```

Routing is unchanged: local `worker.input` is directed to the far-side recipient,
far-side `worker.input` to the local recipient, ids preserved, lifecycle traffic
never relayed as conversation. The `remote-niw` pseudo-type is gone — the
identity a link dials in as is simply a `niq` declaration with
`unmanaged-process` placement, in declarations and in the create form alike.

`hiw` is retired. The WebUI used to borrow a `webui-hiw` worker to speak on the
bus; now the app **is** its worker's backend:

```go
app := webui.New(eventLog, engine, backend, registry, addr)  // no bus handle yet
ui := iface.New(iface.Config{ID: "webui", Type: "interface", Bus: ch,
                             Backends: []iface.Backend{app}, Approvals: true})
ui.Start(ctx)   // SetHandler(app) → start the backend → receive events
app.Bind(addr)  // only now listen on HTTP
```

`webui` is the only worker that is on the bus but not in a declaration: the
assembly joins it, grants its ACL in code, and protects it, so it cannot be
destroyed or suspended and does not appear as a lifecycle row in the roster.

## 5. The WebUI speaks core interfaces only

The project-side bridge (`app/project/webui_bridge.go`) is gone. Each gap it
covered went to the layer that owns it:

| Was | Now |
|---|---|
| read declarations from `workers.json` | `worker.Catalog.Declarations()` |
| translate the create form into spawn arguments | `spawn` accepts nested `params` and flattens them itself |
| write ACL to the file, then mutate the registry | the host's `acl.update` tool → one `ACLAdmin` that does both, in order |

So the app depends on nothing from `app/*`: writes are bus requests to the host,
reads are the injected catalog/registry/event log, and creating a worker is the
same single step an agent uses — validate, declare, start — instead of "write a
declaration, then ask the host to resume it".

`update` and `archive` joined the host's tool surface for the same reason:
a declaration edit is not a lifecycle step, and "stop it, hide its declaration,
drop its identity" is one intent, not four HTTP-handler steps.

## 6. Approvals as a reusable capability

Serving approvals is a *declaration* on the bus — the worker advertises
`approval.request` in its watch contract and must be granted it — so an injected
backend cannot own it, and every outward surface needs the same inbox. It is now
`Config.Approvals` on the interface worker, with `h.(iface.Inbox)` for
`Pending` / `Decided` / `Decide` / `OnChange`.

Recovery is a **snapshot**, like every other worker's state: `Snapshot` writes
`{"approvals": …}` plus the backend's bytes, `Restore` runs before `Start` (so a
request parked before a restart is still answerable), and `OnDurableChange` tells
the assembly when to persist. An app-hosted worker writes the same
`workers/<id>/state.json` as everything else.

## 7. Smaller, sharper

- **A round that goes unanswered gets a watchdog.** A round that dispatched work
  and then heard nothing left the worker waiting forever; it now times out and
  reports instead of stalling.
- **MCP serves every extension off the event loop**, so one slow remote call no
  longer delays a local `mcp.server.list`.
- **A program change is booked, then rendered at a cache reset** — the system
  prompt stays a pure function of applied state, and the cache is only invalidated
  when the cost is already being paid.
- **Remote binds require auth, and auth fails closed**; loopback without
  credentials still works, because that is an explicit local choice.
- **Destroy is immediate.** The approval flow around destroy is deferred rather
  than half-implemented.

## 8. Transport: two bugs found by running it for real

Linking two live instances surfaced both:

- **A replaced stream's teardown must not delete the live session.** It made every
  publish from the replacement fail with `worker not connected`, which the client
  reads as a broken link and reconnects — a flap that never resolves.
- **The SSE stream refuses to reuse a pooled connection** (a connection the server
  has already closed makes `net/http` silently retry, and the retry becomes a
  ghost second attach). The attach log now carries the remote address, which is
  what tells a colliding second client apart from a real reconnect.

And one from the worker side: **a message a backend consumes is not a "no handler"
error**. A forwarded `worker.input` carries a request id, and answering it with
`request.failed` because no *tool* matched was simply wrong.

## 9. Upgrading

Projects are migrated in place on first start — the schema version stays 2:

| Old | New |
|---|---|
| `type: niw` | `type: niq` |
| `niw.*` tool events | `niq.*` |
| `type: hiw`, `id: webui-hiw` | declaration dropped (the worker is app-hosted now) |
| ACL `target`/`source`, `workspace.approver` naming `webui-hiw` | `webui` |
| `type: remote-niw` | `type: niq` + `placement: unmanaged-process` |

Free-text references (`niw.status` written into a program or an instruction) are
logged, not rewritten — rewriting someone's prose is worse than telling them.

Things worth knowing:

- **A pending approval parked in an old `hiw` snapshot is not carried over.** The
  worker it belonged to no longer exists; the requester's own snapshot still holds
  the parked call, so re-ask if it matters.
- **ACL edits now go through the bus** (`acl.update`), which means the host must be
  online. That is the same trade-off suspend/resume already made.
- **One identity, one dialer.** Two clients presenting the same credential on one
  bus evict each other, forever. If several surfaces should watch the same niq,
  give each its own interface worker identity instead of sharing a credential.
- The TypeScript SDK gained `InterfaceWorker` (same `Backend.setHandler`
  contract), so a bridge outside the process — Feishu, WeCom, a notifier — can be
  written without re-implementing request correlation.
