# niq — a worker whose backend is another niq instance

The concrete case of an interface worker (`core/impl/iface`). One side faces this
bus: it is an ordinary managed worker there — identity, ACL, presence, tools. The
other side is a `Backend` that dials **another niq instance's bus** as a plain
HTTP-transport remote worker, presenting the identity + credential that instance
issued for it. The far side sees an ordinary remote worker; it neither knows nor
needs to know this worker type exists.

```
   this bus  ──┬── worker.input ──────────► far-side recipient
               │      (directed, never broadcast)
   far side  ──┴── worker.input ──────────► local recipient
```

| Piece | Where |
|---|---|
| bus participation (loop, tools dispatch, presence) | `iface.Worker` |
| the far-side link, bindings, `niq.*` tools | `Backend` (this package) |
| declaration / spawn / ACL wiring | `app/project/worker/builders.go` (`buildNIQSpec`) |

## Two bindings, both runtime-mutable and durable

- **local recipient** — which local worker receives far-side messages; set by
  local peers through `niq.recipient.set` on this bus.
- **remote recipient** — which far-side worker receives our local `worker.input`;
  set by far-side peers through the same event arriving over the link.

Both are directed sends, never broadcasts, and both persist (the backend
implements `Snapshot`/`Restore`, and the assembly checkpoints on change).

## The security boundary is the far side's ACL

A remote worker must never be free to directed-send into a bus it does not own:
the far side grants this identity exactly the targets it wants reachable, and its
bus enforces that on every `Send`. `BuildRemoteIdentity` (process/provision.go)
narrows a `niq` far-side identity to exactly `worker.input` + `request.*` +
`worker.ready` (publish) and `worker.input` + `worker.discover` (subscribe),
instead of the `*` a generic third-party worker gets.

## Tools

`niq.recipient.set` / `unset` / `get`, `niq.dial`, `niq.status` — plus
`worker.readme`, which renders the live link state and bindings (never the
credential).
