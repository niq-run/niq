# app/standalone — the single-machine form

> The full tree, the ownership rules and the dependency checks are in
> `../../doc/directory-layout.md`.

`app/standalone` is **one deployment form** of niq: a gateway on one port, a
separate process per project, and a shared store on disk. The packages here are
the entry that starts those processes (`cli`), the host that owns them
(`gateway`), the domains they use (`launcher`, `project`, `worker`, `store`,
`provider`, `template`, plus the shared `contract`), and the stdlib-only `helper`
those all sit on. Today this is the only form that exists; `app/cluster` is the
placeholder for the distributed one.

## The packages at a glance

| package | answers only this | must not |
|---|---|---|
| `cli` | how a `niq` invocation becomes a process: `Spec`, argv routing, the gateway daemon, `status` snapshot, completion, update | implement a domain; render the UI; import `project` |
| `contract` | the wire shapes of the project API (`View`, `Instance`, `Ports`) and the HTTP client that speaks them | import anything outside the standard library; know the disk shape |
| `helper` | the stdlib-only primitives every package shares: port-holder discovery/stop, dialable/probe addresses, loopback and ephemeral bind shaping, port parsing | know a project, worker or auth; import anything from `app` |
| `gateway` | the only entry point into niq: auth, `/api/*`, `/p/{id}/` proxy, the `UIMount` table | know a concrete UI; assume the caller is a browser; run a project in its own process |
| `launcher` | instances: `List`/`Start`/`Stop`/`Restart`/`IsRunning`, and the private child-process protocol | render; listen on a port; import `cli` or `app/client` |
| `project` | the project definition (create/list/link/load) and one project's runtime assembly | know other projects; import `launcher` or `gateway` |
| `project/api` | the project's HTTP surface: JSON, SSE, approvals, uploads, programs | serve the SPA; do auth; touch `store` or `worker` |
| `template` | project templates: built-in presets, parse/validate, zip import, instantiation input | know a project instance |
| `store` | where things live and what shape they have: every path, `project.json`'s `Project`, every JSON/SQLite file | import `project`, `launcher` or `app/client` |
| `provider` | provider config *semantics*: `Find`, `Default`, `Build`, `ProviderSources`, the startup gate | read or write files; have instances or a lifecycle |
| `worker` | the worker control plane: registry, builders, params, placement routing, recovery | read global config or assemble paths; import `store` |

The sub-packages are narrower on purpose — each is a boundary someone else
needs, not a folder that grew:

| sub-package | owner of |
|---|---|
| `store/event` | the event store (`event.AppendStore`, SQLite) |
| `store/identity` | `identities.json` as a `bus.Store`, plus `Merge` for legacy seeding |
| `store/migrate` | every reader of an older file format — the only place allowed to know one |
| `worker/process` | OS children: `Supervisor` + credential provisioning |

## Dependency direction

The exact import edges (internal ones only; every package also reaches `core`):

```
cli        contract, gateway, helper, launcher, store, app/client/tui
gateway    helper, launcher, project, provider, store, template
launcher   contract, helper, project, store
project    helper, project/api, provider, store (+event +identity +migrate),
           template, worker (+process)
template   store, worker
worker     provider, worker/process
store      contract, helper, provider, worker, store/migrate
contract   stdlib only
helper     stdlib only
provider   core only (no file IO; secrets expand from the environment)
project/api nothing from app/standalone (core contracts only)
```

Read it as strict layering: `store` never points up, `worker` never imports
`store`, `contract` and `provider` import nothing from this directory, and
the project's HTTP package knows no domain above it.

The one edge that looks like a cycle and is not: `launcher → project` serves
**only** `ChildMain`. The two process boundaries never meet:

```
gateway process:  cli → gateway → launcher.Start(id)
                                     │ exec(self, ChildVerb, id)
                                     ▼
project process:  main → cli.Main → launcher.IsChild → ChildMain → project.RunProject
```

`Start` only execs; `ChildMain` only runs in the new process. That is why
"the gateway runs projects" is false, and why a project nobody's parent is can
still be stopped (it is identified by its ports).

## Who owns what

| thing | sole owner | everyone else |
|---|---|---|
| paths (`NIQ_HOME`, project dirs, file names, auth/provider paths) | `store/root.go` | calls `store.*`; never joins a path |
| address primitives (port parsing, dial/probe, loopback and ephemeral bind shaping, port holders) | `helper` | calls `helper.*`; never re-implements a probe or a port lookup |
| `project.json` (the stored shape) | `store/project.go` | maps it outward; never publishes it raw |
| `/api/projects` (the wire shape) | `contract` (`View`) | gateway encodes what `launcher.ViewOf` built |
| the child argv word (`__project-run`) | `launcher` | `cli` only asks `launcher.IsChild` |
| Basic auth and `common/auth.json` | `gateway` | — |
| human identity, approvals, uploads | `project/api` | the UIs post to it over HTTP |
| provider JSON IO | `store/provider.go` | `provider` receives the parsed `*Config` |
| manifest files (`workers.json`, `workers.archived.json`) | `store/manifest.go` | `worker` sees `ManifestStore` only |

Two project shapes exist on purpose. `store.Project` is the file, `contract.View`
is the response, and `launcher.ViewOf` is the only conversion: it resolves the
directory, probes the port for `running`, and drops the fields clients have no
business seeing. Adding a field to `project.json` therefore changes nothing on
the wire until it is also added to `View`.

`provider` and `store` are a deliberate inversion: `store` imports the domain
*value types* (`provider.Config`, `worker.Manifest`) to serialize them, and the
domains import nothing from `store`. The one function type that crosses back,
`provider.Loader`, is defined in `provider` and implemented by the assembly.

## Where new code goes

| change | package |
|---|---|
| a new HTTP endpoint for the gateway (projects, templates, providers) | `gateway` (`projects.go`, `templates.go`, `providers.go`) |
| a new endpoint for a project's data | `project/api` |
| a new lifecycle action on an instance | `launcher` |
| a new worker type, placement or param | `worker` (`registry.go`, `builders.go`, `params.go`, `backend.go`) |
| a new on-disk file or path | `store` (and `store/migrate` if it replaces an old one) |
| new CLI verb, flag or shell wiring | `cli` |
| a host/port primitive several packages need | `helper` (stdlib only; no domain knowledge) |
| anything a UI needs to know | `contract` first, then `gateway` serves it |

## Checks you can run

```sh
go list -deps ./app/standalone/project/api | grep 'standalone/\(store\|worker\)'  # empty
go list -deps ./app/standalone/store | grep 'standalone/\(project\|launcher\)'    # empty
go list -f '{{join .Imports "\n"}}' ./app/standalone/helper | grep 'standalone/'   # empty
grep -rn 'standalone/store' app/standalone/worker --include=*.go                  # empty
grep -rn 'NIQ_HOME' app/standalone --include=*.go | grep -v '/store/' | grep -v _test  # empty
grep -rn 'ChildVerb' app --include=*.go | grep -v 'app/standalone/launcher/'        # empty
```

The project port is a data port: no `go:embed` in `project/api`, no SPA, no
auth. All of that is the gateway's, which is why there is exactly one exposed
port and one place that decides who may open it.
