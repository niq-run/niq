# `desktop/` — the installable shell (Tauri + `niq` sidecar)

A thin desktop wrapper that makes niq **installable**: it bundles the `niq` CLI
as a sidecar, starts the gateway, and loads the gateway's own WebUI.

There is deliberately **no UI of its own**. The interface is the core
repository's WebUI, served by the gateway, so one UI is maintained once and every
platform gets the same one. This directory is the packaging + process
supervision, nothing else. (A client that wants its own screens belongs in the
`clients` repository instead.)

## Why a shell instead of a native app

- **One UI.** Upgrading the CLI upgrades the interface; there is no second
  front-end to keep in sync.
- **One protocol.** The shell talks to `http://127.0.0.1:9527` exactly like a
  browser does.
- **Install once.** The user gets a working niq (CLI + gateway + UI) from a single
  download, with no separate CLI install step.

## Prerequisites

| Tool | Why |
|---|---|
| Go | to build the `niq` CLI that is bundled |
| Rust ≥ 1.77 (1.84+ for `rustc --print host-tuple`) | Tauri |
| the Tauri CLI (`cargo install tauri-cli --locked` or `npm i -D @tauri-apps/cli`) | to build the bundle |
| Linux only: `libwebkit2gtk-4.1-dev`, `libappindicator3-dev`, `librsvg2-dev`, `patchelf` | the WebKitGTK webview |

## Build

```sh
# from the repository root
make desktop          # builds ./bin/niq, stages the sidecar, runs `cargo tauri build`

# or by hand
cd app/client/desktop
./scripts/prepare-sidecar.sh                  # → src-tauri/binaries/niq-<triple>
cargo tauri build                             # → src-tauri/target/release/bundle/
```

`prepare-sidecar.sh` names the binary with the host's target triple, which is
exactly what `bundle.externalBin` expects:

```
src-tauri/binaries/niq-aarch64-apple-darwin      (Apple Silicon)
src-tauri/binaries/niq-x86_64-apple-darwin       (Intel)
src-tauri/binaries/niq-x86_64-unknown-linux-gnu
src-tauri/binaries/niq-x86_64-pc-windows-msvc.exe
```

To bundle a prebuilt CLI instead of compiling one, point `NIQ_BIN` at it.

## How it boots

```
window created ──> ui/index.html (splash, bundle-local)
                        │  invoke ui_ready
Rust: ensure_running ───┴──> sidecar `niq --local`   (bundle)
                               │  poll /api/projects (max 40s)
                               ▼
                        window.location.replace("http://127.0.0.1:9527/")
                               │
                               ▼
                        the gateway's WebUI (same origin, same API)
```

- **Discovery** (`src-tauri/src/gateway.rs`): `NIQ_GATEWAY_URL` → the CLI's
  `~/.niq/shell.json` → `niq status --json` → `127.0.0.1:9527`. An already
  running gateway is reused, so the shell is safe to launch twice.
- **Startup** prefers the bundled sidecar; a development build without one falls
  back to an installed `niq` (`NIQ_BIN`, then `PATH`).
- **Failure** is shown in the splash (`window.niqFailed`) with a 重试 button,
  which re-runs the whole boot through the `retry_gateway` command.
- **Menu**: 重新加载 / 在浏览器中打开 / 停止网关 / 退出.

Quitting the shell does **not** stop the gateway: it is the CLI's daemon and the
user's terminal (`niq i`, `niq c`) shares it. 停止网关 is explicit.

## Layout

```
app/client/desktop/
├── ui/index.html            the splash (no build step, no framework)
├── scripts/prepare-sidecar.sh
└── src-tauri/
    ├── Cargo.toml           tauri + tauri-plugin-shell
    ├── tauri.conf.json      window, externalBin, bundle metadata
    ├── capabilities/        shell:allow-execute|spawn for the sidecar
    ├── icons/               generated from the shared brand icon
    └── src/main.rs          menu, splash handshake, sidecar spawn
        src/gateway.rs       find / start / probe the gateway
```

## Notes

- The sidecar name in `capabilities/default.json` is `binaries/niq` — the
  `externalBin` string, **not** the file name with the triple.
- The splash ships the WebUI's own logo font subset (`ui/assets`), so the boot
  screen matches the app it becomes.
- A release build needs code signing + notarisation on macOS and signing on
  Windows; the config here is unsigned for local use.
