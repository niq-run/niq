# builtin/workers — one file per built-in worker type

Each file in this directory is one type this deployment form can spawn: the
parameters it accepts, the constructor that turns them into a `SpawnSpec`, and
the registration that pairs the two. `Register` is the explicit list of what
ships.

The framework these register into — `Registry`, `BuildContext`, the contract
machinery, `EnsureIdentity` — is [`builtin`](../README.md) one directory up.
This package depends on it, never the other way round: the registry does not
know a single type name.

```
Register(ctx, reg)
   ├── registerReason     → reg.RegisterType("reason", spawnParamsReason, ctx, buildReasonSpec)
   ├── registerCoding     → …
   ├── registerWorkspace  → …
   └── …  host, timer, niq, program, history, directory, mcp
```

## A type is three things in one file

```go
// reason.go (abridged)
var spawnParamsReason = []string{"instruction", "goal", /* … */}

func registerReason(ctx builtin.BuildContext, reg *builtin.Registry) {
	reg.RegisterType("reason", spawnParamsReason, ctx, buildReasonSpec)
}

func buildReasonSpec(ctx builtin.BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error)
```

The contract and the builder are registered in one call on purpose: what the
builder reads and what the check allows are one edit, and
`TestSpawnParamsCoverEveryBuilder` walks the registry in both directions — a
registered type with no contract, or a contract with no builder, fails. Adding a
type is adding a file here and a line in `Register`.

`type: interface` has no file: its backend is a process-local object the
assembly injects, which no manifest can describe, so there is nothing to
construct. `Registry.Validate` reports `no builder for type` for it.

## The ten

| type | what its builder wires |
|---|---|
| `reason` | the reasoning worker: provider and model resolution, programs, event converters, budgets, a checkpoint hook for a runtime provider switch |
| `coding` | the same family plus `tool_mode` |
| `workspace` | a filesystem backend over its mounts; derives its id from the first mount when none is given |
| `program` | the filesystem `Store` plus the in-process QuickJS `Executor` for a project's programs |
| `host` | the lifecycle control plane itself — its backend is this control plane |
| `timer` | reminders and scheduling back to the caller |
| `mcp` | MCP servers, exposed or fixed, with per-server tool allowlists |
| `niq` | a peer niq instance reached over the HTTP transport |
| `history` | the event store, with list-size knobs |
| `directory` | the read-only fleet roster that answers who is here and what they offer |

Two of them are not conversation peers: `host` and `directory` are infrastructure
the rest of the fleet addresses by tool name.

## Shared plumbing

| File | Responsibility |
|---|---|
| `workers.go` | The package doc and `Register`: the explicit list of types this form ships. |
| `connect.go` | `specConnect` / `aclFor`: the connect closure and bus grants every builder returns, including the identity it ensures. |
| `params.go` | The readers builders share: `ParsePrograms` (exported — the template tooling checks the same projection), `parseMountsParam` (home expansion, absolute paths), `parseEvents`, `pStrings`, `pInt`. |
| `reason_family.go` | The one construction path `reason` and `coding` share: provider resolution through the injected loader, program seeding, budgets, event converters. |

## Files, then

| File | Responsibility |
|---|---|
| `workers.go` | package doc + `Register`. |
| `connect.go` | `specConnect`, `aclFor`. |
| `params.go` | param readers. |
| `reason_family.go` | shared reason/coding inputs. |
| `reason.go` | `buildReasonSpec` + `spawnParamsReason` + `registerReason`. |
| `coding.go` | `buildCodingSpec` + contract + registration. |
| `workspace.go` | `buildWorkspaceSpec` (mounts, approver). |
| `program.go` | `buildProgramSpec` (filesystem + QuickJS). |
| `host.go` | `buildHostSpec` (the lifecycle worker). |
| `timer.go` | `buildTimerSpec`. |
| `mcp.go` | `buildMCPSpec` (servers, allowlists). |
| `niq.go` | `buildNIQSpec` (peer instance over HTTP). |
| `history.go` | `buildHistorySpec` (list knobs over the event store). |
| `directory.go` | `buildDirectorySpec` (the roster). |
| `*_test.go` | Documented payloads accepted, unread/cross-type/identity keys rejected, the coverage invariant, mount parsing, and the builders driven through a real manager. |

## Invariants

- **No manifest, no store.** A builder reads `WorkerConfig.Params` and returns a
  `SpawnSpec`; it never loads a manifest and never resolves a path it was not
  handed (`ProgramsRoot`, `StateDir`, mounts).
- **One type, one file.** Contract, builder and registration together.
- **The id may be derived, and the builder owns it.** `workspace` resolves an id
  from its path; the manager records what the builder produced, not what the
  caller passed.
- **The identity is the type's default**, never an override: an edited grant on
  disk wins.
