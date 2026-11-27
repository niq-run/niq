# niq directory layout

> Status: as-built (2026-11-21). English · [简体中文](directory-layout.zh-CN.md).
>
> This document describes only **where code belongs**. The runtime layout of
> `~/.niq` is described in `install-and-cli.md` §6; the layering principles and
> the target form live in the design docs repo (`design/app/` — `architecture`
> explains why, `application-architecture` explains how it lands), and this file
> is synced once they land.

## 0. One sentence

`core/` holds **contracts** and **mechanisms that do not depend on a deployment
form**; `app/` holds the **forms** (each with its own shell) and the **product
entry**. Dependencies may only point from `app` to `core`, and forms are peers of
one another.

## 1. Source tree

```
niq/
├── core/
│   ├── itfs/                     # contracts: interfaces and value types only — no implementations, no IO
│   │   ├── worker/               #   Lifecycle / Manifest / Backend / WorkerInfo / Placement
│   │   ├── bus/                  #   Identity / Control / Store / Channel
│   │   ├── event/                #   events, match patterns, the reply convention, the event-store contract
│   │   ├── program/              #   the program read/write contract
│   │   └── llm/                  #   the model-call contract
│   └── impl/                     # mechanisms: implementations of the contracts + the worker type library
│       ├── workerctl/           #   live instances: build/spawn/snapshot/suspend/resume/restart/destroy
│       ├── busctl/               #   bus identity control plane (Store injected; the adapter lives in the form)
│       ├── eventbus/             #   engine, registry (runtime identity projection), HTTP transport
│       ├── workers/<type>/       #   coding / directory / history / host / mcp / niq / program / reason / timer / workspace
│       ├── iface/                #   the interface-worker base class
│       ├── baseworker/           #   shared implementation for tools and presence
│       ├── reason/               #   the reasoning engine (transcript, provider adapters)
│       ├── provider/             #   LLM provider implementations
│       └── embedws/              #   the filesystem backend for workspaces
│
├── app/
│   ├── standalone/               # deployment form: single-machine niq
│   │   ├── cli/                  #   the `niq` command tree: routing, gateway daemon (incl. the port-conflict choice), update/completion, Spec
│   │   ├── contract/             #   wire shapes of the project API (View/Instance) + HTTP client; stdlib only
│   │   ├── helper/               #   stdlib-only primitives: port holders, dial/probe, loopback and ephemeral bind, port parsing
│   │   ├── gateway/              #   the only HTTP endpoint: auth, /api/*, /p/{id} proxy, UIMount table
│   │   ├── launcher/             #   instances: List/Start/Stop/Restart + the private child-process entry
│   │   ├── project/              #   project definition + single-project runtime
│   │   │   ├── run.go            #     startup orchestration (owns the signal ctx)
│   │   │   ├── api/              #     project HTTP API (JSON/SSE, approvals, uploads, programs) + its iface.Backend half
│   │   │   ├── project.go        #     create/list/link (reads and writes via store)
│   │   │   ├── export.go         #     project → template export
│   │   │   └── types.go          #     package doc + the project's vocabulary aliases
│   │   ├── template/             #   templates and presets (shared domain)
│   │   ├── store/                #   the only owner of paths and on-disk layout
│   │   │   ├── root.go           #     NIQ_HOME / project dirs / file names / auth and provider paths / address rendering
│   │   │   ├── project.go        #     project.json (the stored project shape)
│   │   │   ├── manifest.go       #     workers.json + worker.ManifestStore
│   │   │   ├── state.go          #     state.json + workerctl.WorkerStore
│   │   │   ├── provider.go       #     provider.json read/write
│   │   │   ├── migrate/          #     old formats → current schema (the only reader of old formats)
│   │   │   ├── identity/         #     the bus.Store over identities.json
│   │   │   └── event/            #     event storage (SQLite)
│   │   ├── provider/             #   shared configuration domain: provider definitions, model resolution, LLM construction
│   │   └── worker/               #   worker control plane (self-contained)
│   │       ├── controlplane.go   #     New / Recover / Stop / HostBackend / Manifest
│   │       ├── backend.go        #     worker.Backend: placement routing + lifecycle
│   │       ├── manifest.go       #     the Manifest model, its lowering to a WorkerConfig, the ManifestStore contract
│   │       ├── builtin/          #     the type table's framework: registry, assembly context, contract machinery, identity
│   │       │   └── workers/      #       one file per built-in type: contract + builder + registration
│   │       └── process/          #     OS child processes: provision + supervisor
│   │
│   ├── client/                   # interfaces: every one of them is a gateway client
│   │   ├── webui/                #   browser interface: assets/ + Assets() (no server)
│   │   ├── tui/                  #   terminal interface: picker, status table, conversation view
│   │   └── desktop/              #   installable shell: Tauri + the CLI as a sidecar, loads the WebUI
│   │
│   ├── cluster/                  # deployment form: distributed (placeholder, a peer of standalone)
│   │   └── README.md
│   │
│   └── publish/                  # distribution: decides which UIs ship and whether the binary is bundled
│       ├── cmd/                  #   the niq CLI: thin main + install.sh + .goreleaser.yaml
│       ├── npm/                  #   npm package and publish scripts
│       └── releases/             #   per-version archive + LATEST.md (the current draft)
│
├── sdk/ts/                       # out-of-process TypeScript SDK (HTTP client + InterfaceWorker)
├── e2e/                          # black-box suites: harness + suites/{cli,bus,worker}, behind the `e2e` tag
├── doc/                          # in-repo docs: install/CLI, build & test, this layout — each in English and Chinese
└── Makefile / go.mod / README.md
```

There are exactly two build output directories; neither is committed and neither
belongs to the layout:

- `bin/`: local executables (`niq` from `make build`);
  `make install` links from here;
- `dist/`: distributable artifacts (goreleaser archives, `niq.app`, zips, the npm
  tree). `goreleaser --clean` wipes it, so executables never live here.

Intermediate files stay out of the source tree: the two architecture slices of a
universal build go to `dist/.build` and are deleted as soon as lipo has run.

## 2. Four kinds of things, four homes

| Kind | Test | Home |
|---|---|---|
| **Contract** | interfaces/value types only; implemented by several parties | `core/itfs/*` |
| **Mechanism** | an implementation that does not depend on the deployment form; reads no project files | `core/impl/*` (`workerctl`, `busctl`, `eventbus`, the worker type library) |
| **Deployment form** | knows placement, the project file layout, the process model, the storage adapter | `app/<form>/` (`standalone`, `cluster`) |
| **Product entry** | assembles one form (`publish`) | top level of `app/` |

`app/standalone/store/root.go` is the form's path knowledge (`NIQ_HOME`, `~/.niq`,
project directories, the auth and provider paths, address rendering). Other
packages call `store.*` and never assemble a path themselves; the gateway's auth
file path is passed in by its caller.

Address parsing and probing sit one level below even that: `app/standalone/helper`
is stdlib-only and owns port parsing, dial/liveness probes, loopback and
ephemeral bind shaping, and port-holder discovery/stop. `store.PortOf` delegates
to it, and the CLI uses it both for liveness and for resolving an occupied
gateway port.

`app/client/*` holds interface implementations with no form knowledge: `webui`
has assets only, `tui` has terminal rendering plus `contract.Client`. Neither
imports anything from `app/standalone` except `app/standalone/contract`. cluster
implements its own interfaces; once it exists, shared frontend components come
out of `app/client/webui/assets`.

Inside the form there are two project shapes on purpose: `store.Project` is what
`project.json` holds, and `contract.View` is what `/api/projects` returns.
`launcher.ViewOf` is the only place that turns one into the other. It resolves
the directory, probes the port for the run state, and publishes only the fields
a client needs, so a field added to the file stays private until someone decides
otherwise.

The declaration a distribution makes is a value, not an environment: `publish/cmd`
builds `cli.Spec{Version, BuildTime, UI}` and hands it to `cli.Main`, which is the
only place the UI mount table enters the form.

## 3. Why `busctl` and `workerctl` are both in core

Both are **form-agnostic**:

- `busctl.Manager` only knows `bus.Identity` plus an injected `bus.Store`
  and touches no filesystem (`go list` shows just `fmt`, `sort` and the
  contracts); standalone injects `store/identity`, and cluster swaps in a KV
  store to reuse it.
- `workerctl.Manager` only knows `WorkerConfig` / `SpawnSpec` / `ManagedWorker`;
  it has no opinion on placement, bus semantics or the type registry.

The worker **control plane** is not here, because the placement set, the builder
table and process supervision are form knowledge — that is why it lives in
`app/standalone/worker`. cluster will bring its own control plane and can reuse
these two libraries.

## 4. Dependency direction (hard rules)

```
app/publish ──▶ app/standalone ──▶ core
     │                │
     └──▶ app/client ──▶ app/standalone/contract
app/cluster ──▶ core
```

- `app/standalone/*` and `app/cluster/*` **never import each other**;
- `app/client/*` imports `app/standalone/contract` and nothing else from the form;
- `app/standalone/contract` imports stdlib only, so any interface — including a
  future remote client — can safely use it;
- `app/standalone/helper` likewise imports stdlib only, so `store` can use its
  address primitives without the layering pointing back up;
- `app/standalone/gateway` knows no concrete UI, only `UIMount`;
- `app/publish` is the distribution layer and may depend on forms and interfaces;
  **forms never import it**.

Checks you can run:

```sh
go list -deps ./app/client/... | grep 'app/standalone' | grep -v '/contract$'  # must be empty
go list -f '{{join .Imports " "}}' ./app/standalone/contract | grep 'niq-run/niq'  # must be empty
go list -f '{{join .Imports " "}}' ./app/standalone/helper | grep 'niq-run/niq'    # must be empty
go list -deps ./app/standalone/... | grep 'app/cluster'               # must be empty
go list -deps ./app/cluster/...    | grep 'app/standalone'            # must be empty
```

## 5. Where new things go

| What you want to do | Where |
|---|---|
| Add a worker type | `app/standalone/worker/builtin/workers`: one file holding its spawn contract, its constructor and its registration |
| Add a placement | constant in `core/itfs/worker` + routing in `app/standalone/worker/backend.go` |
| Add a host/port primitive that several packages need | `app/standalone/helper` (stdlib only; no domain knowledge) |
| Add a bus identity rule | contract in `core/itfs/bus` + `core/impl/busctl` |
| Add instance lifecycle capability | `core/impl/workerctl` |
| Add a deployment form | `app/<form>/`, implementing `worker.Backend` with its own storage and placement |
| Add form-local storage | that form's `store/` subpackage (e.g. `store/identity`) |
| Add a new outward-facing interface | implement `iface.Backend` and have the form assemble an interface worker (no manifest needed) |
| Add a CLI command / release config | `app/publish` |
| Browser interface and frontend | `app/client/webui` (including `assets/`); cluster brings its own UI, and shared components come out of those assets once it exists |

## 6. Three easy misplacements

1. **Importing the paths or layout a `webui` needs into the webui**: the caller
   should pass them in (the auth path is decoupled exactly this way).
2. **Putting one form's file formats into core**: core knows only the `Store`,
   `ManifestStore` and `WorkerStore` interfaces; JSON files belong to the form.
3. **Putting the CLI inside a form's directory**: `publish` is the product binary
   and its artifacts, and cluster commands will hang off the same binary too.
