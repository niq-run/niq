# app/standalone/worker — the standalone worker control plane

This package is the **complete worker control plane** for the standalone
deployment form. It owns every worker's manifest and lifecycle, assembles the
two mechanism libraries it needs, and exposes stable exits for the host worker
and the WebUI. The assembly injects storage providers once; from then on the
control plane recovers, runs and stops on its own.

```
core/itfs/worker           Lifecycle / Manifest / Backend / WorkerInfo
        ▲
        │
app/standalone/worker      ControlPlane + backend + builders + manifest model
        │
        ├── workerctl     (core/impl) live instances: build/spawn/snapshot/…
        └── process        OS children: provision + supervise
```

## Plane composition

| piece | responsibility | where |
|---|---|---|
| `workerctl.Manager` | live instance lifecycle: build, spawn, suspend (snapshot), resume, restart, destroy, checkpoint | `core/impl/workerctl` |
| `process.Supervisor` | OS child lifecycle: launch, crash restart with backoff, stop, shutdown kill-tree | `process/` |
| identity provisioning | mint credentials, register identities with the bus control plane | `process/provision.go` |
| `typeRegistry` | which types exist, how each is built, which params each reads | `registry.go` |
| `ControlPlane` | owns the above, recovers the manifest set, exposes the exits | `controlplane.go` |

Neither library knows about placements. The control plane is what maps a
placement to mechanisms: `in-process` uses the manager, `managed-process` uses
the supervisor plus provisioning, `unmanaged-process` uses provisioning only.

## Manifest model

A manifest is one worker's persisted definition (`workers.json` /
`workers.archived.json`):

```go
type Definition struct {
    ID     string
    Type   string
    Params map[string]any   // the ONLY construction input
}

type Manifest struct {
    Definition
    Placement Placement       // in-process / managed-process / unmanaged-process

    Label, Description string
    Tags []string

    Command []string          // managed-process
    Cwd     string            // managed-process
}
```

Bus identity — publish/subscribe grants and credentials — is **not** here. The
bus control plane (`core/itfs/bus.Control`, implemented by
`core/impl/busctl`) is its single owner. `params` remains the single
construction bag:

| placement | params | validation |
|---|---|---|
| `in-process` | builder construction params | `SpawnParamsByType` |
| `managed-process` | the child's **environment** | flat string map, valid env keys, no `NIQ_*` |
| `unmanaged-process` | not passed | must be empty |

## Files

| File | Responsibility |
|---|---|
| `controlplane.go` | `Config` / `ControlPlane`: `New`, `Recover`, `Stop`, `HostBackend`, `Manifest`, app-hosted state helpers. |
| `declaration.go` | The model, no IO: `Definition` / `Manifest`, `Placement` / `PlacementOf`, display metadata validation, `SanitizeID`, `Instantiate`. |
| `params.go` | `SpawnConfig` / `Spec` / `ManifestFromSpec`, the per-type spawn contract, `ValidateProcessParams`, and param readers. |
| `registry.go` | `typeRegistry`: type label → builder + spawn-parameter check. |
| `builders.go` | `BuildContext`, the type-label constants (`TypeInterface`, `DefaultWebUIID`, `DefaultHostWorkerID`), and one constructor per built-in type; `specConnect` ensures the worker's durable identity. |
| `identity.go` | `ensureIdentity`: register on first connect, keep an edited grant, seed the type default only once. |
| `backend.go` | `worker.Backend`: placement routing, lifecycle verbs, manifest edits, archive, protected ids, per-id serialization. |
| `store.go` | `ManifestStore`: the storage boundary `app/standalone/store` implements. |
| `process/` | Third-party process runtime: its own input contract (`spec.go`), provisioning (`provision.go`), supervision (`supervisor.go`), log/state dir naming (`ids.go`), platform shims (unix/windows). |

Runtime state (`state.json` / snapshots) is `workerctl.WorkerStore`, defined in
`core/impl/workerctl` and implemented by `store.NewState` (`app/standalone/store`).
The direction is one-way by design: `store` imports this package's *value types*
(`Manifest`, `WorkerSet`) so it can serialize them, and this package never imports
`store` — it drives the `ManifestStore` interface declared here and receives a
`*workerctl.Manager` from the assembly.

## Where a backend comes from

Two sources, one rule: **what a manifest can describe, the builder constructs;
what it cannot, the assembly injects.**

| backend | source | why |
|---|---|---|
| `reason`, `coding`, `workspace`, `program`, `mcp`, `timer`, `history`, `directory` | builder, from `params` (goal, mounts, roots, servers, …) | derivable from the manifest |
| `host` (lifecycle), `niq` (far-side link) | builder, from `params` + ambient services (`WorkerBackend`, `Manager`) | params are the input; the rest is process infrastructure |
| interface worker (`webui`, type `interface`) | **assembly**, through `iface.Config.Backends` | its backend is a process-local object (the app), which no manifest can express |

Those ten are exactly what `registerBuilders` registers. `type: interface` is the
eleventh label: no builder, not spawnable.

## Flow

```
ManifestStore.List(false)
      │
      ▼
ControlPlane.Recover
      ├── in-process ....... workerctl.Manager.RecoverAll (snapshot replay)
      └── process .......... backend.Resume → provision → supervisor / identity only

ctx.Done (assembly)
      ▼
ControlPlane.Stop
      ├── manager.SnapshotAll   (while workers are still alive)
      ├── manager.StopAll
      └── supervisor.Shutdown
```

All lifecycle writes enter through `backend.go`, whether from the host worker,
the WebUI, or startup recovery.

## Exits

| exit | shape | consumer |
|---|---|---|
| `HostBackend()` | `worker.Backend` (Lifecycle + Manifest, no ACL) | the host worker |
| `Manifest()` | `worker.Manifest` (Get / List / Update over `WorkerInfo`) | the WebUI |
| HTTP | — | deferred |

## Invariants

- **One-way dependency.** This package never imports `app/standalone/project`.
  Manifests go through `ManifestStore`; the process subpackage never imports
  this package.
- **One construction owner.** `params` is the only construction input; typed
  fields were folded into it by the migration and by the WebUI form.
- **The resolved manifest wins.** A builder may derive an id (workspace from its
  path); the manager records what the builder produced.
- **Identity is the bus control plane's.** Grants and credentials live in the
  bus identity store. The control plane only seeds a grant on first connect and
  never overwrites an existing one.
- **Local process credentials are ephemeral.** managed-process tokens are minted
  per launch and never persisted; unmanaged-process credentials are durable.
- **Validation at the spawn boundary only.** Recovery deliberately skips the
  param check so an accepted manifest is not stranded by a retired key.
- **Legacy lives in the migration.** `store/migrate` converts old project and
  template files once; runtime reads schema 2 only.

## Editing vs. lifecycle

`Update` and `Archive` are the two non-lifecycle verbs. Both take the same
per-id lock as the lifecycle verbs, so an edit cannot interleave with a stop.

- `Update` rewrites the manifest only, and validates the RESULT (display
  metadata limits plus the placement's param contract). Nothing is restarted:
  params are build input, so they apply on the next start.
- `Archive` is one move: stop a live worker, move the manifest to the archived
  file, drop the *runtime* identity (the durable grants stay, so a restore
  resumes with them). Restoring moves it back and starts it again. It is
  idempotent in both directions.

ACL editing is deliberately not here: it is an identity write, performed by the
WebUI against the bus control plane.
