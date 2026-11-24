# builtin — the type table of the single-machine form

`builtin` answers two questions about a worker type, and nothing else: **may this
config be built, and how?** The first is the type's spawn contract (the
parameters it accepts, and the error that names them when one is unknown); the
second is the constructor that turns those parameters into a `SpawnSpec` — the
bus connection, the grants and the live worker instance.

It exists as its own package because it is *assembly knowledge*, not mechanism.
The control plane above it (`app/standalone/worker`) owns the manifest model,
placement routing and lifecycle, and knows a type only as a label it passes
down. `workerctl` receives one `Build` function and never learns that types
exist. A different deployment form can assemble a different table without
touching either.

```
worker (control plane)          manifest model, placement, lifecycle
        │  builtin.Register(ctx, registry)
        ▼
builtin                         per-type contract + builder, the registry
        │
        ├── core/impl/workers/*  the worker implementations themselves
        └── worker/process       only for provisioning types that dial or spawn
```

## One file per type

Adding a type is adding a file and a line in `Register`. Each file holds the
three things that must not drift apart:

```go
// reason.go (abridged)
var spawnParamsReason = []string{"instruction", "goal", /* … */}

func registerReason(ctx BuildContext, reg *Registry) {
	reg.RegisterType("reason", spawnParamsReason, ctx, buildReasonSpec)
}

func buildReasonSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error)
```

The contract and the builder are registered in one call on purpose: the valid
parameter set is the type's own knowledge, so "what the builder reads" and "what
the check allows" are one edit. `TestSpawnParamsCoverEveryBuilder` walks the
registry in both directions — a registered type with no contract, or a contract
with no builder, fails.

`type: interface` is the eleventh label and has no file here: its backend is a
process-local object the assembly injects, which no manifest can describe, so
there is nothing to construct. `Registry.Validate` reports `no builder for type`
for it.

## The registry

`Registry` is the table: type label → builder + parameter check, plus the
accepted-key list each registration recorded. Three seams matter:

| method | who calls it |
|---|---|
| `Build(cfg)` | `workerctl.Manager`, as its one injected `Build` function |
| `Validate(cfg)` | the backend, at the two write boundaries (create, update) |
| `RegisterType(typ, accepted, ctx, build)` | this package's `Register` |
| `RegisterBuilder(typ, build, check)` | the seam for out-of-tree types and tests |

`Validate` is deliberately not called on recovery: a manifest already accepted
must not be stranded by a stricter check added later.

## Shared plumbing

Two files hold what every type needs:

- `build.go` — `BuildContext` (what the assembly injects: identity registry, bus
  control plane, in-process listener, engine, manager, event log/store, provider
  loader, programs root, state dir, and the lifecycle backend the host worker
  drives), plus `specConnect`/`aclFor`, which build the connect closure each
  type's `SpawnSpec` carries.
- `params.go` — the contract machinery (`CheckSpawnParams`, with
  "did you mean …" aliases), the managed-process environment rule
  (`ValidateProcessParams`), and the readers builders share
  (`ParsePrograms`, `parseMountsParam`, `parseEvents`, `pStrings`, `pInt`).

The reason family (`reason`, `coding`) shares one construction path —
`reason_family.go`: provider resolution through the injected loader, program and
event seeding, budgets. That is the only place a provider is resolved, so both
types report a bad `provider`/`model` the same way.

## Files

| File | Responsibility |
|---|---|
| `builtin.go` | The package doc, the three names the assembly references (`TypeInterface`, `DefaultWebUIID`, `DefaultHostWorkerID`), and `Register` — the explicit list of types this form ships. |
| `registry.go` | `Registry`, `NewRegistry`, `RegisterType`, `RegisterBuilder`, `Build`, `Validate`, `Types`, `Accepted`. |
| `build.go` | `BuildContext`, `specConnect`, `aclFor`: the bus connection and grants every builder returns. |
| `params.go` | `CheckSpawnParams` and its aliases, `ValidateProcessParams`, `ParsePrograms`, and the param readers. |
| `identity.go` | `EnsureIdentity`: register on first connect, keep an edited grant, seed the type default only once, re-project a restored worker. |
| `reason_family.go` | The shared reason/coding inputs: provider resolution, program seeding, budgets, event converters. |
| `reason.go` | `buildReasonSpec` + `spawnParamsReason` + `registerReason`. |
| `coding.go` | `buildCodingSpec` + `spawnParamsCoding` + `registerCoding`. |
| `workspace.go` | `buildWorkspaceSpec` (mounts, approver) + contract + registration. |
| `program.go` | `buildProgramSpec` (the filesystem + QuickJS program backend) + contract + registration. |
| `host.go` | `buildHostSpec`: the lifecycle worker whose backend is the control plane itself. |
| `timer.go` | `buildTimerSpec`. |
| `mcp.go` | `buildMCPSpec` (exposed/fixed servers, tool allowlists). |
| `niq.go` | `buildNIQSpec`: a peer niq reached over the HTTP transport. |
| `history.go` | `buildHistorySpec` (list size knobs over the event store). |
| `directory.go` | `buildDirectorySpec`: the read-only fleet roster. |
| `*_test.go` | The table's own tests: documented payloads accepted, unread/cross-type/identity keys rejected, the coverage invariant, mount parsing, and identity seeding. |

## Invariants

- **No manifest, no store.** This package reads `WorkerConfig.Params` and builds
  a `SpawnSpec`; it never loads a manifest and never touches a file path except
  the ones it is handed (`ProgramsRoot`, `StateDir`, mounts).
- **One rule per type, in one file.** A type's accepted keys, its builder and
  its registration sit together.
- **Unknown parameters fail loudly.** A key the type does not read is rejected
  with the list of what it does, before a worker is created and before a
  manifest is persisted.
- **Validation is a write-boundary check.** Recovery skips it.
- **The identity a builder ensures is the type's default**, never an override:
  an edited grant on disk wins.
