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

## Deliveries and failures

- Each `OnEvent` subscription has its own queue and goroutine: a slow or panicking
  backend cannot stall the worker's loop or starve another backend. A subscriber
  that falls behind drops events (and logs it) rather than growing without bound.
- `Request` assigns a trace/request id when the caller omits one, waits for the
  matching `request.completed`, turns `request.failed`/`request.rejected` into a
  `ReplyError`, and fails fast when the worker stops mid-flight.
