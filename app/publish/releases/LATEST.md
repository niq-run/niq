# niq v0.17.0

A release about where things live. Almost nothing here is a new capability: the
repository is reorganised around what a deployment form *is*, and the pieces that
had been sharing a home were pulled apart — one HTTP surface, one auth point, one
place that knows about addresses. Then three black-box suites were written against
the result, which is how a process-killing race in the bus got found.

The sentence the whole release orbits: **a deployment form is a directory, and
every interface is a client of it.**

## Highlights

- **`app/` is three kinds of thing.** `app/standalone` is the single-machine form,
  `app/client` is every interface, `app/publish` is distribution. `core` keeps
  contracts and form-agnostic mechanisms, and imports none of them.
- **The gateway is the only HTTP surface.** One auth point, one proxy rule, one
  mount table. A project port serves JSON/SSE and nothing else.
- **The worker subsystem closes over its own control plane**, and bus identities
  get a durable owner of their own (`identities.json`).
- **`helper` is the one library for addresses.** It turns an address into a dial
  target, asks whether something answers, and finds who holds a port — so an
  occupied gateway port becomes a choice instead of a bind error.
- **A project's stored shape is not its wire shape.** `store.Project` is the file;
  `contract.View` is the response, and a conversion is the only bridge.
- **`app/client/mac`** (was `menubar`), bundle `run.niq.mac`, next to `webui` and
  `tui` and symmetric with the `app/publish/mac` channel.
- **58 black-box cases** behind `make e2e`, running in ~8 s.

## 1. `app/` is three kinds of thing

Before, `app/` was a pile of peers: `control`, `niqhome`, `project`, `webui`,
`menubar`, `publish`. Nothing said which of them was a deployment form, which was
a UI, and which was packaging — so every new file had to guess.

```
before                          after
app/control                     app/standalone/{cli, gateway, launcher,
app/niqhome                       project (+api), store (+event, identity,
app/project                       migrate), worker (+process), provider,
app/webui                         template, contract, helper}
app/menubar                     app/client/{webui, tui, mac}
app/publish                     app/cluster   (placeholder sibling)
                                app/publish/{cmd, mac, npm, releases}
```

What each move was for:

- **`control` → `gateway` + `cli`.** The old package mixed the HTTP surface with
  the daemon lifecycle. They split: `gateway` is the only HTTP endpoint (auth,
  `/api/*`, the `/p/{id}` proxy, the UI mount table) and never runs a project
  itself; `cli` owns argv, the daemon's lifecycle, `status`, completion, update.
- **`webui` → three places.** Assets to `app/client/webui` (no server), the
  project's JSON/SSE API to `app/standalone/project/api`, auth to `gateway`.
  `shell/webui` and `project/api` used to be one server, which is why a project
  port could host a SPA.
- **`project` → `app/standalone/project`**, `project/worker` →
  `app/standalone/worker`: a form owns its assembly, not just its domain.
- **`niqhome` → `app/standalone/store`.** Paths and file IO were a peer of the
  forms; they are now one package the form depends on, with `store/{migrate,
  identity, event}` under it.
- **`menubar` → `app/client/mac`**, `publish/cmd`'s command tree →
  `app/standalone/cli` (publish keeps `main.go`, `install.sh`, goreleaser), and
  `publish/release-notes` → `publish/releases`.
- **`core` stays `{itfs, impl}`.** The one rename inside it is `workersvc` →
  `workerctl` (live instances), so it reads as a peer of `busctl`.

The rules that came out of it, now written down in `doc/directory-layout.md`:

- **The gateway is the only HTTP surface.** Every UI reaches niq through it, so
  there is one auth point, one proxy rule, one place to add a domain.
- **A project is a data port.** `project/api` speaks JSON/SSE and binds loopback
  only; no SPA, no auth.
- **A UI is a client.** `app/client/*` imports `contract` and nothing else from
  the form, so `webui`, `tui` and the macOS app are the same kind of thing.
- **Distribution declares, it does not assemble.** `publish/cmd` hands
  `cli.Spec{Version, BuildTime, UI}` to `cli.Main`; the mount table enters the
  form there and nowhere else.
- **One binary, several process identities.** `main` dispatches once on the
  private child verb; the CLI front-end, the gateway daemon and each project
  child are different processes with different composition roots.

## 2. The CLI is a shell of shells

`niq` no longer has a project command family. A project is something a shell does:
the browser shell and the terminal shell both list, pick, stop and restart, and
the CLI's job is to start the gateway those shells talk to.

| v0.16.0 | v0.17.0 |
|---|---|
| `niq control [-f]` | `niq w [-f]` — the browser shell |
| `niq project` / `niq p` | `niq i` — the interactive terminal shell |
| `niq project list/create/run` | gone; project operations live in a shell |
| `niq project list --ids` | `niq status --ids` (or `--json`) |
| `--webui-auth` | gone — the project API is loopback-only |
| `control.json` | `shell.json` — the shell's state file |

Bare `niq` still starts the gateway in the background and reports on it when it is
already running, so the common path is unchanged.

## 3. The worker subsystem is a self-closed control plane

**Layering**

- `app/standalone/worker.ControlPlane` owns `Recover` and `Stop`: it lists the
  manifest store itself (in-process first, then process workers), and shutdown no
  longer blocks waiting for a signal.

**Contracts (`core/itfs/worker`)**

- One runtime interface `Lifecycle` (now including `Archive`) plus one declaration
  interface `Manifest` (`Get` / `List(archived)` / `Update`) over a single
  `WorkerInfo` view.
- `Backend = Lifecycle + Manifest`; `Editor`, `Archiver`, `Catalog`, `Decl`,
  `ACLAdmin`, `RecoverOptions`, `Backend.Recover` and `Backend.Run` are deleted.
- `host` depends on `worker.Backend` alone, and no longer exposes `acl.update` —
  the bus event world has no permission write entry.

**Mechanism libraries**

- `core/impl/workersvc.WorkerService` → `core/impl/workerctl.Manager`: live
  instances only, one injected `Build` function; the type registry moved to the
  control plane.
- `ProcessSupervisor` → `process.Supervisor`.
- Neither library knows what a placement is; the control plane maps placement to
  mechanism.

**Bus identities (`core/impl/busctl`)**

- New `bus.Control` + `bus.Store` over an injected JSON file store; `Seed()`
  projects durable identities before the bus serves.
- Grants and credentials move out of `workers.json` into `identities.json`;
  `migrateInlineIdentities` lifts legacy inline ACLs once.
- WebUI ACL editing is a single `UpdateACL` call (store plus runtime projection),
  no bus round-trip.

**Terminology**

- `Declaration`/`DeclStore` → `Manifest`/`ManifestStore`; the embedded definition
  type is `Definition` (the old `Config`).
- Data files (`workers.json`, `workers.archived.json`, `state.json`) and the three
  placement strings are unchanged.

## 4. A project has two shapes

`contract.Project` was three things at once — `project.json`, the body of
`/api/projects`, and a struct every client imported — so a field added to the file
appeared on the wire.

- `store.Project` is the file; `contract.View` is the response; `launcher.ViewOf`
  is the only conversion. It resolves the directory and probes the run state, so a
  field stays private until someone publishes it.
- `gateway` and `project` no longer import `contract` at all.
- `contract.Instance.WebUIAddr` was `json:"-"`: set on the server, never reached a
  client. Removed.
- `project/api/backend.go` → `iface.go`, named for the interface it implements
  (`iface.Backend`) rather than for a "server-side backend" that does not exist in
  that package. Tests collapsed to one file per source file.

## 5. Addresses, and an occupied port

Four packages had grown their own copy of the same three ideas — turn an address
into a dial target, decide whether something answers, find who holds a port — and
they disagreed where copies do.

- New `app/standalone/helper`, stdlib only: `Port`, `DialAddr`, `IsServing`,
  `IsServingPort`, `ProbeListen`, `IsLoopbackBind`, `LoopbackBind`,
  `EphemeralAddr`, `ValidPort`, plus `Holders` / `Signal` / `Kill` (listeners only,
  never itself, never pid ≤ 1). It sits under `store`, which needs address parsing
  but must not point upward; `store.PortOf` delegates to it.
- Replaced private copies in `cli` (`probeAddr`/`isServing`), `launcher`
  (`probePort`/`probeAddr`), `project` (`validPort`/`resolvePort`/`loopbackBind`/
  `ephemeralAddr`) and `gateway` (`IsLocalOnlyAddr`).
- **An occupied gateway port is a choice, not a bind error.** `gateway.Bind` wraps
  `EADDRINUSE` in `PortInUseError`; `cli/ensure.go` decides before the child is
  spawned: our own gateway is reported rather than offered as a kill target, a
  dynamic port is skipped, a non-`EADDRINUSE` failure (permission, bad address) is
  passed through, and with a terminal the operator picks stop-the-holder / another
  port / cancel. Without a terminal the conflict becomes an error naming the
  holder. Stopping is `SIGTERM`, a bounded wait, then `SIGKILL`. Every start path
  goes through it, `restart` included.

## 6. The macOS client is `client/mac`

`app/client/menubar` was the source of the whole app — entry point, plist, icon —
while naming only the one surface it ships today. With no users yet, the rename
goes all the way through:

- `app/client/menubar` → `app/client/mac`, next to `webui` and `tui`.
- `run.niq.menubar` → `run.niq.mac`; bundle executable `niq-menubar` → `niq-mac`;
  release archive `niq_mac_darwin_universal.zip`; the release job and the npm
  packaging follow.
- Unchanged on purpose: the bundle is still `niq.app`, the launcher is still
  `miq`, and `LSUIElement` still hides it from the Dock — it is a menu bar item
  until a window says otherwise.

The npm package carries that bundle. The packaging script's macOS guard was
testing a variable the rename no longer set, which under `set -u` stops the job
outright — after goreleaser has published the release, so the archives ship and
the npm packages never do.

## 7. What the suites found

- **`niq i` linked to the wrong address.** A project row printed the project's own
  loopback port, which serves `/api` only — no index, no HTML — so the link opened
  a 404. `contract.ProjectURL` builds the gateway address (`/p/<id>/`, served from
  the embedded bundle whether the instance runs or not) and both the picker and the
  `status` table use it. With no gateway there is no such address, so the table
  leaves the cell empty instead of pointing at a data port.
- **A CJK project id shifted the table.** Ids were padded by byte length;
  `SanitizeID` only swaps separators, so a Chinese name counted triple.
  `displayWidth` counts terminal cells.
- **`status --json` and the table disagreed** about the project link (one
  hand-built the path, unescaped). One builder now.
- **The bus could kill the project process.** Routing canonicalized each event once
  so the delivered and persisted copies were one snapshot — but handlers are
  allowed to write into the payload they receive (`baseworker` hands it over as the
  tool arguments; the timer decorates `tick_type`, the workspace decorates an
  approval reply). `json.Marshal` walking that map while a handler wrote it is
  `concurrent map iteration and map write`, a runtime fatal, so the `recover`
  around routing could not help. The log now keeps a snapshot taken **before**
  delivery: history records what was routed, and the encoder cannot race a writer.
  Repro: repeated dial-in worker creation killed the project at ~58 iterations; 260
  iterations now run clean.
- Smaller: completion scripts stopped advertising the removed `project` verb (and
  the zsh script had a stray `;;` that made `_niq` fail to parse); `menu`/`readKey`
  take their input as arguments, so the keymap and the redraw rules are testable.

## 8. End-to-end suites

`e2e/` is black-box: it builds the CLI and drives it the way a user does, so it
fails when behaviour changes rather than when a signature does. 58 cases, ~8 s,
behind the `e2e` build tag (plain `go test ./...` is unchanged), `make e2e`.

| suite | drives | asserts |
|---|---|---|
| `cli` (33) | the binary | start/stop/restart, idempotent start, stale record, `:0` vs fixed address, invalid address, port conflict naming the holder, `--version`/`--help`, no escape codes in a pipe, completion scripts (parse and install), `/p/<id>/` link, CJK alignment, `stop -p`, picker without a TTY |
| `bus` (13) | a dial-in worker over the real HTTP transport | publish and subscribe grants, dial-in accepted with the issued credential and rejected with a wrong one, reconnect after the stream drops, directed send vs broadcast, per-connection ordering, query by request id, request/reply correlation, failure reply, transient delivered but not stored, SSE feed |
| `worker` (12) | the bundled worker types | timer fires and lists what it scheduled, workspace mounts edited and refused off a non-workspace, directory roster/search/describe, program round-trip and delete, host suspend/resume/destroy, one reason turn against a stub chat-completions server |

Isolation is the part worth keeping: every test gets its own `NIQ_HOME`, `HOME` and
`XDG_CONFIG_HOME`, a `PATH` whose `open`/`npm` only record the call, and a gateway
on `127.0.0.1:0`. A sandbox stops what it started at cleanup — including a gateway
that `niq w` or `niq i` started on their own, which is how the first version leaked
three of them.

## 9. Upgrading

Projects migrate in place as before; the schema version stays 2, and no data file
is renamed.

**Stop the old gateway first.** The daemon's state file is now `shell.json`
(`control.json` in v0.16.0), so a v0.16.0 gateway is invisible to the new CLI: it
will see 9527 occupied by a process it cannot identify as its own. Run `niq stop`
with the old binary before upgrading. If you forget, the new CLI names the holder
and offers to stop it — that is the port prompt working, not a failure.

Other things worth knowing:

- **The CLI verbs changed** (§2). Scripts that used `niq project …` need
  `niq status --ids` / `--json` instead, and project operations belong to a shell.
- **macOS**: reinstall from `niq_mac_darwin_universal.zip`. The bundle identifier
  is `run.niq.mac`, so the old `niq.app` is a different application — remove it
  rather than running both menu bar items.
- **Go import paths** moved with the packages (`app/project` →
  `app/standalone/project`, and so on). Nothing is expected to import them from
  outside the repository, but a local build that does will need the new paths.
- **The TypeScript SDK** (`sdk/ts`, `@niq.run/worker-sdk` 0.9.0) is versioned
  separately from the CLI and is not published by the release workflow. Its
  `iface` half gained `Runnable`, `ToolHost` and extension registration, mirroring
  the Go interface worker.
- `--auth` is now the single credential path: it protects a non-loopback bind and
  is saved to `~/.niq/common/auth.json`. There is no second, per-UI auth.
