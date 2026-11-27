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
| manifest / spawn / identity wiring | `app/standalone/worker/builders.go` (`buildNIQSpec`) |

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

## `niq.dial` persists by default

A dial may repoint the link with `remote_url` / `remote_worker_id` /
`remote_credential`. That change is **persisted before it is applied**, so the
next process start dials the same target — an agent that provisions a link does
not have to re-provision it after a restart.

It goes through the worker's own declaration writer, not around it:

```
niq.dial → baseworker.PatchConfig → injected writer → control plane Update
                                                       (merge, validate, lock, log)
```

The writer is injected by the assembly at construction (`iface.Config.PatchConfig`
→ `baseworker.SetConfigPatcher`), wired to `WorkerBackend.Update`, so the
control plane keeps ownership, validation and the "applies on the next start"
contract. The patch is a **merge** of only the keys the call changed, so an
unrelated param edited elsewhere is never clobbered.

- No writer injected (embedded use, tests): `PatchConfig` returns
  `baseworker.ErrConfigUnavailable`, the repoint applies to this process only,
  and the reply says "not saved".
- A failed write fails the call and leaves the old target in force: memory and
  config never disagree.
- The credential is persisted **as configured**. Pass a reference
  (`env:NAME`, `file:PATH`) to `niq.dial` and it stays a reference; the resolved
  token is never written — not to the manifest, and not to the worker's snapshot
  (which holds only the two recipient bindings, and is plain JSON on disk).
  Resolution goes through the injected credential provider (`app/standalone/vault`), so a
  dedicated `vault:` provider can replace the default env/file one later.
