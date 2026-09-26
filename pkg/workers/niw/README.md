# NIW — niq interface worker

NIW links **two niq instances**. On the local bus it is an ordinary
host-managed worker; at Start it dials another niq instance's bus as a plain
HTTP-transport remote worker, using a worker_id + credential that the remote
instance issued to it. From the remote side it is just a normal remote worker —
the remote instance neither knows nor cares that "niw" exists.

Its signal flow is modelled on the **lark worker**: an external endpoint is
bound to one internal worker via a **directed send, never a broadcast** — and,
like lark's `lark.reason.*`, each binding is **mutable at runtime via
`niw.recipient.*`**.

A single NIW carries a bidirectional link with two bindings:

- **`localRecipient`** — the local worker that far-side events are directed to.
  Set by LOCAL peers.
- **`remoteRecipient`** — the far-side worker that local `worker.input` is
  directed to. Set by the REMOTE side (it decides who on its bus receives our
  input).

```
A bus                              B bus
peers ─worker.input→ niw ──directed──→ remoteRecipient (set by B)
                    ▲ ◄──events────────┘
                    └── worker.input → localRecipient (set by A)
```

## Responsibilities

- **Local → remote**: NIW subscribes to `worker.input` and DIRECTS it to the
  far-side `remoteRecipient` (set by the far side), never broadcasting.
- **Remote → local**: events the far side routed to this identity are delivered
  as a `worker.input` DIRECTED to the local `recipient`. Far-side bookkeeping
  (`worker.ready` / `gone` / `discover`) is dropped.
- **Runtime switching** (`niw.recipient.set` / `unset` / `get`): the same event
  sets a different binding by which channel it arrives on — local channel sets
  `localRecipient`; remote channel sets `remoteRecipient`. So the far side
  re-binds who on its own bus receives A's input. Both bindings are persisted
  (`Snapshot`/`Restore`).
- **Correlation**: RequestId / TraceID are preserved in both directions.

## Security: the far side's PublishAllow is the boundary

A remote worker must never be free to directed-send into a bus it does not own.
This is an existing bus capability: the FAR side configures the remote
identity's `PublishAllow` to permit exactly the targeted delivery it wants
(e.g. `{type: worker.input, target: <that one B worker>}`), and every `Send` is
enforced against it.

## Config (build.go params)

- `remote_url` — base URL of the remote bus (required)
- `remote_worker_id` — identity to present on the remote bus (required)
- `remote_credential` — credential the remote instance issued; a literal value,
  or `env:VAR` / `file:PATH` reference to keep it out of project.json
- `recipient` — seed for `localRecipient` (optional; changed at runtime)

The local `publish` / `subscriptions` params narrow the local ACL; by default
it subscribes to `worker.input` and its local `PublishAllow` is `*` (like the
reason worker — the binding is runtime-mutable, so the worker can't update its
own ACL per change).