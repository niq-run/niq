# worker/process — the third-party process runtime

This subpackage owns the **third-party process side** of the worker subsystem:

- **Process supervision** for `managed-process` workers — the OS processes the
  host launches and supervises.
- **Bus identity / credential provisioning** for both process placements — a
  child the host launches, and a worker that dials the bus on its own.

It is deliberately independent of the manifest model in the parent
`app/standalone/worker` package. The parent lowers a `Manifest` into the two
input types here; this package never imports it.

```
app/standalone/worker            manifest, params, builders, store, lifecycle
        │  lowers to Spec / IdentitySpec
        ▼
app/standalone/worker/process    supervisor + provisioning
```

## Modules

| File | Responsibility |
|---|---|
| `spec.go` | The input contract: `Spec` (launch: id/type/command/env/cwd/credential), `IdentitySpec` (provisioning: id/type, and the grants that seed a brand-new identity). `Spec.Env` is derived by the caller from the manifest's params. |
| `supervisor.go` | `Supervisor`: launch, crash-restart with backoff, `stdout.log`, stop/restart/remove/shutdown, `ProcessStatus`. It knows nothing about manifests, placements or credentials. |
| `provision.go` | `BuildRemoteIdentity`; `ProvisionManagedProcess` (grants are durable, the token is minted per launch into the temp tier and never persisted); `ProvisionUnmanagedProcess` (durable token + identity); `RegisterTempIdentity`; `ResolveSecret`. It knows nothing about process lifecycle. |
| `process_unix.go` / `process_windows.go` | Platform shims: run the child in its own process group and kill the whole tree (Unix), direct-child kill (Windows). Separate files because Go build constraints are file-scoped — both implementations cannot coexist in one file. |
| `ids.go` | The directory-name sanitizer used for log/state dirs. Mirrors `worker.SanitizeID`; kept local so this package does not depend on the manifest model. |
| `supervisor_test.go` | Real process supervision: backoff restart, stop, manual restart, single-instance replacement, shutdown, stdout log, `NIQ_STATE_DIR`. |
| `provision_test.go` | Default ACLs, temp vs durable identity, credential reuse, grant persistence across relaunch, `ResolveSecret`. |

## Identity ownership

Both provisioning paths write through `core/itfs/bus.Control` — the single owner
of identity data:

- A **managed-process** identity is durable for its *grants* (so an ACL edit
  survives a restart) but carries no persisted credential; the token is minted
  per launch into the temporary tier.
- An **unmanaged-process** identity is fully durable, credential included, so a
  remote worker can reconnect across restarts.

## Why a subpackage

- It uses real `os/exec` and the bus credential machinery; keeping it separate
  lets those be tested with real child processes, without a project file.
- The parent's `backend.go` is the only orchestrator: it decides *when* a
  process is launched or provisioned, while this package only knows *how*.
- The dependency is one-way: parent → process. The package's inputs carry only
  what it needs, so the manifest model can evolve without touching it.
