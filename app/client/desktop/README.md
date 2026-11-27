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
make dist-desktop     # release artifacts (dmg + zipped app) under dist/

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
    ├── Cargo.toml           tauri + shell + notification plugins
    ├── tauri.conf.json      externalBin and bundle metadata; the window is built in code
    ├── capabilities/        sidecar spawn and notification permissions
    ├── icons/               PNG/ICO/ICNS + tray.png/tray.svg; see the notes
    └── src/main.rs          window, menus, menu bar item, splash, sidecar spawn
        src/gateway.rs       find / start / probe the gateway
        src/notify.js        notification clicks back to the page
```

## Notes

- **Network.** The first build downloads crates from crates.io. On a network where
  that is blocked, point cargo at a mirror in `~/.cargo/config.toml` (never in
  this repo):

  ```toml
  [source.crates-io]
  replace-with = "mirror"
  [source.mirror]
  registry = "sparse+https://<your-mirror>/index/"
  ```

- The sidecar name in `capabilities/default.json` is `binaries/niq` — the
  `externalBin` string, **not** the file name with the triple.
- The splash ships the WebUI's own logo font subset (`ui/assets`), so the boot
  screen matches the app it becomes.
- **Notifications.** A macOS webview has no `window.Notification`, so the WebUI's
  browser notification path reported "unsupported" inside the shell even though it
  works in a browser. `tauri-plugin-notification` installs a system-backed
  `window.Notification` at document start; `src/notify.js` runs after it (a
  webview's own initialization scripts are appended after the plugins') and wraps
  it so a click runs the page's own `onclick`. That is the whole adaptation — the
  WebUI is written against the standard API and is not modified.
- **Menus.** Both the menu at the top of the screen and the menu bar item are
  Tauri's own APIs (`tauri::menu`, `tauri::tray`), thin wrappers over NSMenu and
  NSStatusItem; there is no hand-written AppKit module. The menu bar icon is a
  template image (`icons/tray.png`, drawn from `icons/tray.svg`): the mark alone
  without its tile, so macOS tints it with the menu bar's own colour, and
  `tray-icon` draws it at 18 pt. At 18 pt the 3D mark's dark faces disappear, so
  the menu bar uses a flat one instead — three squares in the 品 arrangement,
  which stays legible at that size. The app icon keeps the shaded cubes.
- **Closing the window does not quit.** The close is intercepted
  (`WindowEvent::CloseRequested` → `prevent_close` + `hide`), so the shell keeps
  running with the window put away, and the WebUI keeps its state. Quitting is
  explicit: ⌘Q or 退出 in the menu bar item. The window comes back from 显示窗口
  or by clicking the Dock icon (`RunEvent::Reopen`, macOS only).
- A release build needs code signing + notarisation on macOS and signing on
  Windows; the config here is unsigned for local use.
- **Size.** The release profile optimises the shell for size (`lto`, one codegen
  unit, `opt-level = "z"`, `strip`), which puts it at ~4 MB, and the CLI sidecar
  is built with `-s -w` like the released binary (~21 MB). A macOS bundle is
  therefore ~25 MB and the DMG ~10 MB — nearly all of it the CLI, which is the
  product.
- **Icons.** `icons/icon.icns` is a full-bleed squircle: the tile touches all
  four edges and only the corners are transparent (radius ≈ 22% of the canvas).
  A flat *opaque* square makes macOS 26+ composite the art onto its own light
  plate and shrink it, which reads as an icon that does not fill its tile. The
  PNGs and the `.ico` stay square and opaque for Windows and Linux.
