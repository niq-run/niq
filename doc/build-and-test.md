# Build and test

> Status: as-built (2026-11-21). English · [简体中文](build-and-test.zh-CN.md).
>
> How to build the binaries on this machine, how to run the Go and TypeScript
> suites, how to work on the WebUI with hot reload, and how to dry-run the
> release pipeline. Installing is in [install-and-cli.md](install-and-cli.md);
> where code belongs is in [directory-layout.md](directory-layout.md).

## 1. Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | 1.26+ (`go.mod` says `1.26.0`) | everything; the embedded QuickJS engine requires it |
| Node | 20+ | building the WebUI bundle, the TypeScript SDK |
| Xcode command line tools | any recent | the macOS menu bar app only (Swift + `lipo`) |

Go alone is enough to build, vet and test the backend: **no Node is needed to
compile the CLI**, because the SPA is embedded as committed files.

## 2. The one rule that surprises people

The WebUI bundle is **committed** to the repo (`app/client/webui/assets/dist`,
`go:embed`-ed by `app/client/webui`). So:

- `go build ./...` and `make build niq` work with **no Node and no npm ci** — they
  take the bundle exactly as it is on disk;
- rebuilding the bundle is its own step: `make build webui` (or `npm run build`
  in `app/client/webui/assets`);
- `make build` on its own does everything this machine can build, in order:
  bundle → CLI → app (the app part only on macOS).

Because the bundle is compiled in, a frontend change is only visible after it is
rebuilt **and** the binary is rebuilt — and the commit still matters for
`go build ./...`, `go test ./...` and the release workflow (goreleaser and CI do
not run Node), so commit the rebuilt `app/client/webui/assets/dist` with the
change.

## 3. Make targets

```sh
make build              # everything this machine can build: webui, niq, desktop (with Rust)
make build webui        # just the bundle
make build niq          # just the CLI, embedding the bundle as it is
make build niq webui    # the bundle, then the CLI — order of the words does not matter
make install            # link what is already built into $(PREFIX); never builds
```

The word after `build` is a **target, not an argument** — make runs all of them
— so `make niq` and `make webui` work on their own too, and
`make build` alone still means everything. When `niq` and `webui` are named
together the bundle is built first regardless of the order typed.

| Target | Output | Notes |
|---|---|---|
| `make build` | everything below | `webui niq`, plus `desktop` when Rust + the Tauri CLI are installed |
| `make webui` | `app/client/webui/assets/dist` | `npm ci` then `tsc -b && vite build`; the result is committed |
| `make niq` | `./bin/niq` | injects `-X main.version` (from `git describe`) and `main.buildTime` |
| `make desktop` | a Tauri bundle under `app/client/desktop/src-tauri/target/release/bundle/` | the installable shell: bundles `./bin/niq` and opens the gateway's WebUI; needs Go + Rust + the Tauri CLI |
| `make dist-desktop` | `dist/niq-desktop_<version>_<arch>.{dmg,app.zip}` | release artifacts; `UNIVERSAL=1` for both architectures (`app/publish/desktop/release.sh`) |
| `make desktop-sidecar` | `app/client/desktop/src-tauri/binaries/niq-<triple>` | stages the CLI without building the bundle |
| `make install` | `$(PREFIX)/niq` symlink + `niq.app` in `$(APP_DIR)` (default `/Applications`) | links the CLI, installs completion, and copies the built app if there is one (`APP_DIR=$HOME/Applications` for a no-sudo install) |
| `make snapshot` | `dist/` | full release dry run, no upload (see §7) |
| `make npm-publish` | npm packages | `DRY=--dry-run` to preview; needs a built `dist/` |

`make install` never builds: if `./bin/niq` is missing it says so and stops, so
an install cannot quietly turn into a long npm/vite run.

## 4. Go: build, vet, test

```sh
go build ./...
go vet ./...
go test ./...            # ~6 s on a laptop
```

Tests are hermetic: anything that needs a data root points `NIQ_HOME` at a
`t.TempDir()`, so a test run never reads or writes your real `~/.niq`. Server-side
tests start **real listeners on ephemeral ports** (`127.0.0.1:0`) rather than
mocking the HTTP layer — the gateway, the project API, the bus transport and the
worker supervisor are exercised as the processes/links they really are.

Useful forms:

```sh
go test ./app/standalone/gateway -count=1 -v          # one package, no cache
go test ./app/standalone/project -run TestRunProject  # one test
go test ./core/impl/eventbus/ -count=1                # the bus engine
go test -race ./app/standalone/...                    # concurrency-sensitive packages
```

`-count=1` matters when you have just changed a fixture on disk: Go caches
results per package.

Where the tests live, roughly by area:

| Area | Packages with tests |
|---|---|
| entry and instances | `app/standalone/cli`, `launcher`, `project`, `project/api`, `gateway` |
| worker control plane | `app/standalone/worker`, `worker/builtin` (+ `worker/builtin/workers`), `worker/process` |
| storage and config | `app/standalone/store`, `store/migrate`, `template` |
| mechanisms | `core/impl/eventbus` (+ `transport/httptrans`), `workerctl`, `busctl`, `iface`, `embedws` |
| workers | `core/impl/reason`, `core/impl/workers/*` |
| providers | `core/impl/provider/{openai,openairesponses,anthropic}` |

### End-to-end suites

Everything above tests a package from inside. `e2e/suites/` is different: it
builds the CLI and drives it the way a user does — argv in, exit codes and
output out, state on disk, HTTP on a real port. There is no import of the
packages under test, so a suite fails when behaviour changes rather than when a
signature does.

They are behind the `e2e` build tag, so `go test ./...` stays fast:

```sh
make e2e                       # every suite
make e2e-cli                   # assembly: start/stop/restart, ports, output, completion
make e2e-bus                   # data plane: dial-in peers, delivery, ACL, request/reply
go test -tags e2e ./e2e/suites/cli -run TestE2EStop -v
```

Three suites, split by the layer they observe:

| suite | what it drives | what it asserts |
|---|---|---|
| `cli` | the binary, as a user does | exit codes, output, `shell.json`, the port |
| `bus` | a dial-in worker over the real HTTP transport | directed delivery, broadcast, ACL edits on a live connection, request/reply, transient vs durable |
| `worker` | the bundled worker types | timer fires, workspace mounts, directory roster, program store, host lifecycle, one reason turn against a stub model |

Each test gets its own `NIQ_HOME`, its own `HOME`, and a `PATH` whose `open` and
`npm` do nothing, and starts gateways on `127.0.0.1:0` — so the suites run in
parallel on a machine that is already using niq. A sandbox stops whatever it
started before the test ends, including a gateway that `niq i` or `niq w`
started on its own.

## 5. WebUI development

The bundle is a Vite app in `app/client/webui/assets`.

```sh
make build webui     # one-shot production build (tsc -b && vite build)
```

For iteration, run the dev server with the gateway already up:

```sh
cd app/client/webui/assets
npm run dev          # vite on :5173
```

The dev server proxies API traffic to the gateway on `:9527`, so both pages work
with hot module reload:

- the gateway's own UI: http://localhost:5173/
- a project's UI: http://localhost:5173/p/&lt;id&gt;/ (that project must be running)

Reload the dev page after a gateway restart, and remember that a *project* page
loads the bundle of the process serving it — so a stale project keeps showing the
old UI until you restart that instance (`niq restart -p`).

## 6. TypeScript SDK

The out-of-process SDK lives in `sdk/ts` and has its own suite (Vitest):

```sh
cd sdk/ts
npm ci
npm test             # vitest run
npm run typecheck    # tsc --noEmit
npm run build        # tsc -p tsconfig.build.json → dist/ (not committed)
```

`npm run release` runs test + typecheck + build, refuses a dirty working tree,
and publishes — it is the SDK's own release path, not part of the CLI release.

## 7. Release pipeline, locally

No upload, no publish:

```sh
make snapshot        # goreleaser release --snapshot --clean, then npm publish --dry-run
```

It builds the CLI archives for linux/darwin/windows (amd64 and arm64) into
`dist/` and assembles the npm tree so you can inspect exactly what a release
would ship. The actual publishing (tag push, GitHub Release, mac job) is in
`.github/workflows/release.yml`; `make npm-publish` publishes the npm packages
from an existing `dist/` (`DRY=--dry-run` to preview).

## 8. What is covered today

- **Go unit/integration tests: 109 `_test.go` files, 727 tests**, green in ~6 s
  (~18 s under `-race`). They cover the gateways' HTTP contracts, the launcher's
  process protocol, the bus engine and transports, worker lifecycle and placement,
  storage/migration, the provider adapters, and the worker type library.
- **Black-box suites: 22 `_test.go` files, 58 cases** in
  `e2e/suites/{cli,bus,worker}`, green in ~8 s. They are the only coverage of the
  process boundary — binary → `niq` → gateway → `/p/{id}/` → project process
  (§4).
- **TypeScript SDK: 8 Vitest files, 64 tests** (`sdk/ts`).

Coverage grows with the code: a new command adds a case to `e2e/suites/cli`, a new
worker type adds one to `e2e/suites/worker`.

## 9. CI

`.github/workflows/ci.yml` runs on every push to `dev`/`main` and on every pull
request. It publishes nothing — that is `release.yml`'s job — and its five jobs
run in parallel:

| job | runner | what it proves |
|---|---|---|
| `go` | ubuntu, macOS | `go build`, `go vet`, `go test -race ./... -count=1` |
| `e2e` | ubuntu, macOS | `go vet -tags e2e ./e2e/...`, then `make e2e` |
| `cross` | ubuntu | `GOOS=windows` (amd64, arm64), `linux/arm64` and `darwin/amd64` still compile |
| `sdk` | ubuntu | `npm ci`, `npm test`, `typecheck`, `build` in `sdk/ts` |
| `webui` | ubuntu | the committed bundle is what `vite build` produces (§2) |

Why those choices:

- **`-race` is part of the job, not an extra.** The bus is a concurrent event
  system; a race there kills a process rather than failing a test, so a suite that
  passes without the detector proves less than it looks. A push that supersedes an
  in-flight run cancels it (`concurrency`), which is what keeps that affordable.
- **Both platforms, not just Linux.** The parts that differ are the ones this
  product is about: process signals, port holders, loopback binds, `open` vs
  `xdg-open`. Linux-only CI would leave the macOS half of `helper` untested.
- **`cross` exists because the release builds six archives.** A `_windows.go` file
  that stops compiling is invisible on the runners above and would only surface at
  tag time.
- **The bundle check enforces §2.** `go build ./...` passes with a stale embedded
  bundle, so the rule "commit the rebuilt `dist`" needs something that fails when
  it is forgotten.

Every job is reproducible locally by running the command in its row: none needs a
secret, a service container or a prepared environment.
