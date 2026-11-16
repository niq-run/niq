# iface — the interface worker

A worker whose capability is **reaching outward**: a user interface, an API
application, a chat-platform bridge, another niq instance. It faces the bus like
every other worker (identity, ACL, presence, discover, tools); behind it holds
one or more `Backend`s, the same way a workspace worker holds a filesystem
backend and a reason worker holds an LLM.

```
        bus  ──┬── inbound: events addressed to this worker
               │        ├── request.* replies  → resolve a pending Request
               │        ├── its own tools      → extension registry (answers)
               │        └── everything else    → fan out to Backends
               └── outbound: Handler{Send, Broadcast, Request}
```

## The two halves

| Half | Owner | Why |
|---|---|---|
| bus participation | `iface.Worker` | it is a worker: identity, ACL, presence, tools |
| everything outward | the injected `Backend` | only the backend knows how to render, connect, or persist |

The `Backend` receives its `Handler` through `SetHandler`, called **once, before
`Start`**, so it can build its outward side already knowing how to reach in.
Calls go both ways: the worker injects the handler, the backend calls it.

## What it is NOT

- **not a bus control plane**: editing a worker's ACL, rendering the global event
  stream, injecting an event into it — those belong to the layers above. Routing
  them through the interface worker adds a hop and hides what they are.
- **not stateful itself**: `Snapshot`/`Restore` delegate to the injected
  `Stateful` (a bridge's bindings), so what survives a restart is exactly what the
  backend owns.

## Approvals (opt-in)

`Config.Approvals` turns on the human-in-the-loop inbox. It is a switch rather
than a backend because serving approvals is a DECLARATION on the bus: the worker
advertises `approval.request` in its watch contract (with a schema) and must be
granted it — and a backend cannot register watch entries.

```go
if inbox, ok := h.(iface.Inbox); ok {   // h is the injected Handler
    inbox.Pending(); inbox.Decided()
    inbox.Decide(ctx, requestID, approved, note)   // publishes approval.decision
    inbox.OnChange(func() { /* refresh a badge, pop a notification */ })
    inbox.Seed(requests, decisions)                // recovery: replay the log
}
```

Recovery is a **snapshot**, like every other worker's state: `Worker.Snapshot`
writes `{"approvals": {...}}` (plus the backend's bytes under `"backend"` when it
is stateful), and `Restore` puts it back before `Start`, so a request parked
before a restart is still answerable. `Config.OnDurableChange` fires whenever the
inbox moves — the assembly owns the store and decides when it lands (the WebUI's
app-hosted worker persists to the project's `workers/webui/state.json`).

Replay from the event log is deliberately NOT used: it has no bound (how far back
do you replay?). A snapshot is exactly the state the worker last had.

## Deliveries and failures

- Each `OnEvent` subscription has its own queue and goroutine: a slow or panicking
  backend cannot stall the worker's loop or starve another backend. A subscriber
  that falls behind drops events (and logs it) rather than growing without bound.
- `Request` assigns a trace/request id when the caller omits one, waits for the
  matching `request.completed`, turns `request.failed`/`request.rejected` into a
  `ReplyError`, and fails fast when the worker stops mid-flight.
