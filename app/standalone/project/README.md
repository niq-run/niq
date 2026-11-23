# project — the project definition and one project's composition root

A **project** is niq's unit of work: one directory under `<niq root>/projects/<id>/`
holding the declarations and runtime state of a single swarm. This package owns
that definition on disk — create / list / link / load, plus export back to a
template — and, in `run.go`, the assembly that brings exactly one project up in
its own process: its bus, event store, worker control plane, provider preflight,
WebUI and declared workers.

`project.json` is metadata only (`schema_version`, `id`, `created_at`, `ports`,
`upload_dir`). Worker declarations are the project's source of truth and live in
`workers.json`. The same directory holds `workers.archived.json`,
`identities.json`, `root_secret`, `workers/<id>/` runtime state, `events/events.db`
and `programs/`.

## Where it sits

The package sits on the project side of the process boundary only:

```
launcher ──exec(exe __project-run <id>)──► cli.Main ──► launcher.ChildMain
                                                              │
                                                              ▼
                                                     project.RunProject
                                                              │
        ┌───────────────┬──────────────┬───────────────┬──────┴─────────┐
      store        project/api     worker(+process)  store/event    provider, template
```

`ChildMain` calls `SetupLogging`, then `RunProject`; spawning a child is
`launcher`'s. This package imports neither `launcher` nor `gateway` and knows no
project but the one it runs.

## The definition

`CreateProject` refuses an existing id, creates the directory, writes
`project.json`, and writes `workers.json` by instantiating each template
declaration via `worker.Instantiate` (expands `{project}`); given a template name
it also copies that template's `programs/` tree in. `LoadProject`/`SaveProject`
wrap `store`; `ListProjects` skips unreadable entries and sorts by id.

`LinkProject` symlinks an external directory into the projects root; the id is the
sanitized base unless the directory's `project.json` names one. `ensureProjectJSON`
runs `migrate.EnsureProject` *before* rewriting the file, then creates the metadata
or backfills the id, leaving worker files untouched. `ExportProjectTemplate` turns
active manifests into a validated template (archived excluded, empty errors);
`ExportProjectPrograms` copies the programs tree or reports `false`.

## Running one project

`RunProject` converts legacy files once, loads `project.json` and `workers.json`,
resolves ports, and `runAssembly` builds the process in this order:

| step | built from |
|---|---|
| identity registry and signer | layered registry; `root_secret`, generated once |
| bus identity control plane | `identities.json` + `busctl`, seeded for archived ids before serving |
| event store and engine | SQLite at `events/events.db` (else in-memory); an `EventLog` feeds SSE |
| in-process listener and state store | host-managed workers attached to the engine; `store.NewState` over `workers/` |
| HTTP bus and supervisor | `httptrans` server and the process supervisor, when a bus address is set |
| worker control plane | manifests, state, registry, bus, signer, engine, supervisor; host and WebUI ids protected |
| provider gate | `provider.EnsureLLMConfigured` over declared in-process workers; seeds the example on failure |
| recovery | `ControlPlane.Recover` over the declared set |
| WebUI | `api.New(...)` joined as the interface worker's backend, then restored, bound and served |

Ports are stable and authoritative: persisted values are reused, out-of-range
values dropped with a warning, a stale holder killed before binding, and resolved
ports written back only when they change. The bus binds on all interfaces; the
WebUI binds loopback only. `RunProject` returns once the interrupt stops the
control plane.

## Files

| File | Responsibility |
|---|---|
| `project.go` | The file-backed definition: `CreateProject`, `LoadProject`, `SaveProject`, `ListProjects`, `LinkProject`, `ensureProjectJSON`, and the `SchemaVersion`/`Project` aliases. |
| `run.go` | The composition root: `SetupLogging`, `RunProject`, `runAssembly`, the joined WebUI worker, the root secret, and the port helpers (`freeProjectPorts`, `InterruptPortHolders`, `bindServer`). |
| `types.go` | No code — the aliases that keep the project's vocabulary (`WorkerConfig` → `worker.Manifest`, `TemplateConfig` → `template.Template`). |
| `export.go` | Project → template export: `ProjectProgramsDir`, `ExportProjectTemplate`, `ExportProjectPrograms`. |
| `project_test.go` | Definition behaviour: create/load/list/duplicate, placeholder expansion, archived moves, template program seeding (built-in preset loadable by a program backend), legacy migration on link. |
| `run_test.go` | The concrete stores behind a real control plane: `store.NewState` suspend/recover/destroy and `store.NewManifest` add/list/archive/delete. |
| `export_test.go` | Export round-trip: params and the launch spec survive, archived workers do not, params travel verbatim, programs copy, a workerless project errors. |

## Invariants

- **One project per process.** `ProjectID` is the identity; nothing here holds or
  consults another project's state.
- **The process boundary is `launcher`'s.** This package provides `RunProject`
  and never imports `launcher` or `gateway`, so the child argv word and protocol
  have exactly one owner.
- **`project.json` is metadata only.** Worker declarations are read from
  `workers.json`; linking backfills the id but never rewrites the worker files.
- **Ports are persisted and authoritative.** A reused port is not re-allocated; a
  failed bind falls back to an ephemeral address and the result is saved.
- **Schema 2 only at runtime.** `store/migrate` converts an older project once,
  before anything reads it.
- **The WebUI worker is joined, not spawned.** Its backend is the process-local
  app, which no manifest can describe.
- **Startup failures are always readable.** Every entry path calls
  `SetupLogging`, so a failure lands in the project's daily log.
