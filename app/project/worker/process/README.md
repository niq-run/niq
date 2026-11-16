# app/project/worker/process — third-party process runtime

This subpackage owns the **third-party process side** of the worker subsystem:

- **Process supervision** for `managed-process` workers — the OS processes the
  host launches and supervises.
- **Bus identity / credential provisioning** for both process placements — a
  child the host launches, and a worker that dials the bus on its own.

It is deliberately independent of the declaration model in the parent
`app/project/worker` package. The parent lowers a `Declaration` into the two
input types here; this package never imports it.

```
app/project/worker            declaration, params, builders, store, lifecycle
        │  lowers to Spec / IdentitySpec
        ▼
app/project/worker/process    supervisor + provisioning
```

## Modules

| File | Responsibility |
|---|---|
| `spec.go` | The input contract: `Spec` (launch: id/type/command/env/cwd/credential), `IdentitySpec` (provisioning: id/type/credential/ACL/token TTL), `CredentialStore` (persist a minted credential back to the declaration). `Spec.Env` is derived by the caller from the declaration's params. |
| `supervisor.go` | `ProcessSupervisor`: launch, crash-restart with backoff, `stdout.log`, stop/restart/remove/shutdown, `ProcessStatus`. It knows nothing about declarations or credentials. |
| `provision.go` | `BuildRemoteIdentity`, `ProvisionManagedProcess` (short-lived token + in-memory identity; TTL is process lifetime, not configurable), `ProvisionUnmanagedProcess` (durable token + identity + credential persistence; `token_ttl_seconds` applies), `RegisterIdentity` / `RegisterTempIdentity`, `ResolveSecret`. It knows nothing about process lifecycle. |
| `process_unix.go` / `process_windows.go` | Platform shims: run the child in its own process group and kill the whole tree (Unix), direct-child kill (Windows). Separate files because Go build constraints are file-scoped — both implementations cannot coexist in one file. |
| `ids.go` | The directory-name sanitizer used for log/state dirs. Mirrors `worker.SanitizeID`; kept local so this package does not depend on the declaration model. |
| `supervisor_test.go` | Real process supervision: backoff restart, stop, manual restart, single-instance replacement, shutdown, stdout log, `NIQ_STATE_DIR`. |
| `provision_test.go` | Default ACLs, temp vs durable identity, credential reuse/persistence, `ResolveSecret`. |

## Why a subpackage

- It uses real `os/exec` and the bus credential machinery; keeping it separate
  lets those be tested with real child processes and a fake credential store,
  without a project file.
- The parent's `backend.go` is the only orchestrator: it decides *when* a
  process is launched or provisioned, while this package only knows *how*.
- The dependency is one-way: parent → process. The package's inputs carry only
  what it needs, so the declaration model can evolve without touching it.
