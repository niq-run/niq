# builtin — the framework of the type table

`builtin` is what a worker type registers into. It owns the registry, the
assembly context a builder reads, the spawn-parameter contract machinery, and
the bus identity a builder ensures — and nothing about any particular type.

The types themselves are one directory down, in
[`builtin/workers`](workers/README.md): one file per type. The split is the
point — a table is *a registry plus entries*, and a different deployment form
can keep this framework and ship a different set of entries.

```
worker (control plane)      manifest model, placement, lifecycle
        │  workers.Register(ctx, registry)      ← the entries
        ▼
builtin/workers             one file per type: contract + builder + registration
        │
        ▼
builtin                     Registry, BuildContext, contract machinery, identity
        │
        ├── core/impl/workers/*   the worker implementations themselves
        └── worker/process        for the types that need an OS child
```

## The registry

`Registry` is the table: type label → builder + parameter check, plus the
accepted-key list each registration recorded.

| method | who calls it |
|---|---|
| `Build(cfg)` | `workerctl.Manager`, as its one injected `Build` function |
| `Validate(cfg)` | the backend, at the two write boundaries (create, update) |
| `RegisterType(typ, accepted, ctx, build)` | the entries, through `workers.Register` |
| `RegisterBuilder(typ, build, check)` | the seam for out-of-tree types and tests |

`Validate` is deliberately not called on recovery: a manifest already accepted
must not be stranded by a stricter check added later.

`Build` receiving one function is why `workerctl` never learns that types
exist — and why the entries can move without touching it.

## The contract machinery

`CheckSpawnParams(typ, accepted)` turns a type's accepted-key list into the
validator `RegisterType` installs: an unknown key is rejected with the list of
keys the type does read, plus the "did you mean `instruction`?" aliases for the
mistakes worth pointing at. `ValidateProcessParams` is the separate rule for the
one placement whose params are not construction input but the child's
environment (a flat string map, no `NIQ_*`).

## The identity a builder ensures

`EnsureIdentity(control, identity)` is the connect-time entry point every builder
shares: register on first connect, keep a grant an operator edited, seed the
type's default only once, and re-project a worker whose runtime record was
dropped by an archive. It is exported because `RegisterBuilder` exists for types
this package does not ship, and their builders need the same path.

## Files

| File | Responsibility |
|---|---|
| `builtin.go` | The package doc and the three names the assembly references (`TypeInterface`, `DefaultWebUIID`, `DefaultHostWorkerID`). |
| `registry.go` | `Registry`, `NewRegistry`, `RegisterType`, `RegisterBuilder`, `Build`, `Validate`, `Types`, `Accepted`, and the `SpecFunc` signature a type's builder has. |
| `context.go` | `BuildContext`: what the assembly injects into a builder — the bus control plane and in-process listener it connects through, the instance manager for checkpoints, the provider loader, the programs root, the event store, and the lifecycle backend the host worker drives. A field is here only if some builder reads it. |
| `params.go` | `CheckSpawnParams` + its aliases, `ValidateProcessParams`. |
| `identity.go` | `EnsureIdentity`. |
| `identity_test.go` | Seeding vs. an edited grant, and the restore path. |

## Invariants

- **No type knowledge.** Nothing here names a worker type; the constants are the
  three ids the *assembly* needs, and the interface type is named only to say it
  has no builder.
- **`BuildContext` carries only what a builder reads.** It is an injection bag,
  not a mirror of the assembly: a field nobody reads is removed rather than
  filled.
- **One build function out.** `Registry.Build` is the single `Build` that
  `workerctl` sees.
- **Validation is a write-boundary check.** Unknown parameters fail before a
  worker exists and before a manifest is persisted; recovery skips the check.
- **A stored grant is authoritative.** `EnsureIdentity` seeds a default only
  when none was ever recorded.
