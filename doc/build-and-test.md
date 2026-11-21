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
make build              # everything this machine can build: webui, niq, (miq on macOS)
make build webui        # just the bundle
make build niq          # just the CLI, embedding the bundle as it is
make build niq webui    # the bundle, then the CLI — order of the words does not matter
make install            # link what is already built into $(PREFIX); never builds
```

The word after `build` is a **target, not an argument** — make runs all of them
— so `make niq`, `make webui` and `make miq` work on their own too, and
`make build` alone still means everything. When `niq` and `webui` are named
together the bundle is built first regardless of the order typed.

| Target | Output | Notes |
|---|---|---|
| `make build` | everything below | `webui niq` on Linux, `webui niq miq` on macOS |
| `make webui` | `app/client/webui/assets/dist` | `npm ci` then `tsc -b && vite build`; the result is committed |
| `make niq` | `./bin/niq` | injects `-X main.version` (from `git describe`) and `main.buildTime` |
| `make miq` | `dist/niq.app`, `./bin/miq` | macOS only; packaging is `app/publish/mac/build.sh` |
| `make miq-universal` | `dist/niq.app` (arm64 + x86_64) | macOS only |
| `make dist-miq` | `dist/niq_mac_darwin_universal.zip` | what a release ships |
| `make install` | symlinks in `$(PREFIX)` (default `~/.local/bin`) | links `./bin/niq` (and `./bin/miq` if built), then installs completion |
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
| worker control plane | `app/standalone/worker`, `worker/process` |
| storage and config | `app/standalone/store`, `store/migrate`, `template` |
| mechanisms | `core/impl/eventbus` (+ `transport/httptrans`), `workerctl`, `busctl`, `iface`, `embedws` |
| workers | `core/impl/reason`, `core/impl/workers/*` |
| providers | `core/impl/provider/{openai,openairesponses,anthropic}` |

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

## 8. What is covered today, and what is growing

- **Go unit/integration tests: 105 `_test.go` files**, all green in ~6 s. They
  cover the gateways' HTTP contracts, the launcher's process protocol, the bus
  engine and transports, worker lifecycle and placement, storage/migration, the
  provider adapters, and the worker type library.
- **TypeScript SDK: 8 Vitest files** (`sdk/ts`).
- **No CI test job yet**: the only workflow is `release.yml`, which builds and
  publishes. Tests are run locally.
- **No end-to-end suite yet.** The process boundary (binary → `niq` → gateway →
  `/p/{id}/` → project process) is currently exercised only by unit tests that
  start real listeners in-process.

Both are actively growing: unit coverage follows new code, and an e2e suite for
the real multi-process path is planned. This section will document how to run it
(`make` target, artifacts, which ports it may bind) once it lands, rather than
speculating about it here.
