# app/project/worker — the project's worker subsystem

> 中文说明：`../../../../doc/design/worker/README.zh-CN.md`。

This package owns a worker **as a declaration and a lifecycle**: what a
declaration looks like, what its params mean, which params each type accepts,
how a type is constructed, how third-party processes are supervised and
provisioned, and how create / suspend / resume / restart / destroy are executed.

The project's durable configuration lives in two files:

```
workers.json            active declarations (type / params / placement / identity)
workers.archived.json   archived declarations (same shape)
root_secret             token-signing secret (project root)
```

There is **no `id/identities.json`**. The runtime `IdentityRegistry` is an
in-memory projection seeded from `workers.json`; local-process credentials are
minted in memory and never persisted. `app/project/migrate` is the only code that
reads the legacy shape.

```
core/itfs/worker            Placement / WorkerSpec / WorkerInfo / Backend
        ▲
        │
app/project/worker          declaration / params / builders / store / lifecycle
        │
        ├── process         third-party process supervision + provisioning
        ▲
        │
app/project/template        templates are seeds of this model
        ▲
        │
app/project                 workers.json, project metadata, assembly, WebUI bridge
```

## Declaration model

```go
type Config struct {
    ID     string
    Type   string
    Params map[string]any   // the ONLY construction input
}

type Declaration struct {
    Config
    Placement Placement       // in-process / managed-process / unmanaged-process

    Label, Description string
    Tags []string

    Publish         []PublishSpec       // bus identity/ACL, persisted here
    Subscriptions   []SubscriptionSpec
    Credential      string              // unmanaged-process only
    TokenTTLSeconds *int64              // unmanaged-process only

    Command []string          // managed-process
    Cwd     string            // managed-process
}
```

`params` is the single construction bag:

| placement | params | validation |
|---|---|---|
| `in-process` | builder construction params | `SpawnParamsByType` |
| `managed-process` | the child's **environment** | flat string map, valid env keys, no `NIQ_*` |
| `unmanaged-process` | not passed | must be empty |

## Modules

| File | Responsibility |
|---|---|
| `declaration.go` | The model, no IO: `Config` / `Declaration`, ACL spec types, `Placement` / `PlacementOf`, display metadata validation, `SanitizeID`, `Instantiate`. |
| `params.go` | The single params bag: `SpawnConfig` / `Spec` / `DeclFromSpec`, the per-type spawn contract (`SpawnParamsByType`, `CheckSpawnParams`), `ValidateProcessParams`, and param readers (`parseMountsParam`, `ParsePrograms`, `parseEvents`, `pStrings`, `pInt`). |
| `builders.go` | The builder registry and every type constructor: `BuildContext`, `RegisterBuilders`, `specConnect` (identity registration with the declaration ACL or type defaults), the reason family, and one builder per remaining type. |
| `process/` | Third-party process runtime: `Spec` / `IdentitySpec` inputs, `ProcessSupervisor`, platform shims, and identity/credential provisioning. It never imports the declaration model. |
| `store.go` | The declaration storage boundary: `DeclStore`, implemented by `app/project` over workers.json. Runtime state uses `workerhost.WorkerStore` (defined once in the in-process engine) and is injected through `BackendConfig.StateStore`; the concrete project layout lives in `app/project/store.go`. |
| `backend.go` | The lifecycle backend: placement routing, create / suspend / resume / restart / destroy, catalog, recovery, remote-identity seeding, protected ids, per-id serialization. |

## Flow

```
workers.json (Declaration)
      │ ports / ACL / launch spec + params
      ▼
   params.go        type contract + readers
      │
      ▼
  builders.go       per-type construction
      │
      ├── in-process ......... WorkerService (snapshot in store.go)
      ├── managed-process .... process/: params → env, provision + supervise
      └── unmanaged-process .. process/: provision only, no local process
```

All lifecycle writes enter through `backend.go`, whether from the host worker,
the WebUI, or startup recovery.

## Invariants

- **One-way dependency.** This package never imports `app/project`. Declarations
  go through `DeclStore`; the process subpackage never imports this package.
- **One construction owner.** `params` is the only construction input; the old
  typed fields (`instruction`, `provider`, `mounts`, …) are folded into it by
  the migration and by the WebUI form.
- **The resolved declaration wins.** A builder may derive an id (workspace from
  its path); the lifecycle records what the builder produced.
- **ACL is declaration data, registry is projection.** `publish` /
  `subscriptions` persist in workers.json; the in-memory registry is rebuilt
  from it.
- **Local process credentials are ephemeral.** managed-process tokens are minted
  per launch and never persisted; only unmanaged-process credentials live in
  workers.json.
- **Validation at the spawn boundary only.** Recovery deliberately skips the
  param check so an accepted declaration is not stranded by a retired key.
- **Legacy lives in the migration.** `app/project/migrate` converts old project
  and template files once; runtime reads schema 2 only.

## Tests

| File | Covers |
|---|---|
| `params_test.go` | Spawn contract, ACL spec lowering, process-env validation, `DeclFromSpec`. |
| `builders_test.go` | Mounts/path, NIW params, builders↔contract coverage, hiw construction, in-process lifecycle harness. |
| `backend_test.go` | All three placements: derived id, create/suspend/resume/restart/destroy, protected ids, catalog, real child process. |
| `process/supervisor_test.go` | Crash restart, stop, manual restart, single-instance replacement, shutdown, stdout log, state dir. |
| `process/provision_test.go` | Default ACLs, temp vs durable identity, credential reuse, secret resolution. |

## Non-goals

No project storage (`app/project`), no templates (`app/project/template`), no
capability roster (directory worker), no WebUI form logic.
