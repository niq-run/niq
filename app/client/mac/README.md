# `app/client/mac` — the native macOS client (niq.app)

A SwiftUI app that is a **gateway client**, exactly like the WebUI and the TUI
(see [`doc/how-to-build-a-client.md`](../../../doc/how-to-build-a-client.md)). It
opens a full window that reproduces the WebUI, and keeps the previous menu bar
app's functionality as a `MenuBarExtra` in the same binary.

It talks only to the gateway: `/api/projects…` for the control plane, and
`/p/{id}/api/*` (JSON + SSE) for a project's data plane. It never runs a project,
never implements auth, and never discovers ports.

## Build & run

```sh
make miq                      # or: app/publish/mac/build.sh build
./bin/miq                     # opens dist/niq.app
# or: open dist/niq.app
```

`build.sh universal` / `build.sh zip` still produce the universal + packaged
bundles. The build compiles every `.swift` under `Sources/` with `swiftc` and
assembles `dist/niq.app`.

### Toolchain note (important)

This machine has **Command Line Tools only** (no Xcode). That has one concrete
consequence for how this app is written:

- The **SwiftUI macro plugins are not in CLT** (`libSwiftUIMacros.dylib` is
  missing), so `@State`, `@Environment(Type.self)` and `@Bindable` **do not
  compile**. Use the Combine-style state instead: `ObservableObject` +
  `@StateObject` / `@ObservedObject` / `@EnvironmentObject` / `@Published`
  (these are property wrappers, not macros) — that is what this app does.
- **Liquid Glass compiles fine** against the CLT SDK: `glassEffect(_:in:)`,
  `GlassEffectContainer`, `glassEffectID(_:in:)`, `.buttonStyle(.glass)` (see
  `Sources/Design`). It is gated with `if #available(macOS 26.0, *)` and falls
  back to `regularMaterial`.
- Installing full Xcode would allow the modern `@Observable`/`@State` style, but
  is **not required** to build or run this app.

Deployment target: **macOS 26.0** (for Liquid Glass).

## Runtime notes (learned the hard way)

- **ATS blocks cleartext HTTP by default.** A native `URLSession` cannot reach
  `http://localhost:9527` until the bundle allows it — this is why the first run
  showed "nothing works" while `curl`/the browser were fine. `Info.plist` sets
  `NSAllowsArbitraryLoads` + `NSAllowsLocalNetworking`. (It is a local tool that
  only talks to your own gateway; tighten to `localhost` exception domains if
  you prefer.)
- **SSE frames are separated by a blank line.** `URLSession`'s `AsyncBytes.lines`
  is unreliable about emitting that empty line, so `GatewayClient.stream` parses
  raw bytes and splits on `\n`, treating an empty line as the frame boundary
  (stripping a trailing `\r`). Without this, the watermark/events never fire.
- **App Nap.** A client keeps a long-lived SSE connection, so `AppDelegate` holds
  a `ProcessInfo.beginActivity(...)` to keep the process awake when the window is
  not frontmost.
- **Gateway discovery.** `NiqCLI.status()` (`niq status --json`) if the binary is
  found, else the daemon's own `~/.niq/shell.json` (no PATH needed). Override
  with `NIQ_GATEWAY_URL`; point at a specific binary with `NIQ_BIN`.
- **Logs.** `[niq]` lines go to stderr (run the binary from a terminal to see
  them). `NIQ_DEBUG=1` also prints `[gc]` SSE-frame traces.

## Directory map (responsibilities)

```
app/client/mac/
├── README.md                 this file: architecture, conventions, roadmap
├── Info.plist                bundle metadata (regular app + menu bar item)
├── AppIcon.icns
└── Sources/
    ├── Core/                 no UI: wire models, gateway HTTP/SSE client, CLI locator
    ├── State/                AppModel — presentation state, orchestrates Core
    ├── Design/               design system: palette/typography tokens + Liquid Glass helpers
    ├── Shell/                the window shell: sidebar (nav, filter, settings) + detail router
    ├── Features/             one folder per surface (Talk/Events/Workers/Projects/Models/Approvals/Settings)
    └── App/                  @main, scenes (Window + MenuBarExtra + Settings), menu bar view
```

Each folder has its own `README.md`.

## Dependency direction

`App → Shell → Features → State → Core`, and `Design` is a leaf used by the UI
layers. `Core` must not import SwiftUI/AppKit beyond `Foundation`; `State` must
not import view code. Keep the arrows one-way.

> Today this is a **single module** (all of `Sources/` compiles together) to keep
> the CLT/`swiftc` build and the existing universal packaging simple. The folder
> boundaries above are the intended seams; promoting `Core` to its own SwiftPM
> target later is a mechanical move (add `public`), with no design change.

## Execution order & status

**Implemented (the WebUI alignment pass):**

1. **Talk (core):** the WebUI's message rendering — right-aligned human cards
   (sender · @targets · mode · time · 调用链), left reply cards (big blue worker
   name, quoted incoming message, Markdown body with code fences/tables, 复制,
   context-usage line), thinking disclosures, tool cards (`请求 [type]` with
   code/参数/请求结果 merged from the terminal `request.*` reply), notices
   (abort/timeout/cancel/interrupted), and folded runs of process events
   (`N 条过程事件`). Live token streaming, history backfill from the stream
   watermark, and a composer with recipient chips, 附件 (stub) / 停止 / 模式 / 发送.
2. **Sidebar (WebUI shape):** 项目视图 / 项目资源 sections, 工作单元选择 —
   a searchable checklist that **filters the conversation's events** (展开 opens
   the full picker, 查看全部 includes non-partner workers), 对话视图设置 toggles
   (流式模式 / 展开思考 / 折叠过程事件 / 仅回复), 资源 (project switcher +
   start/stop/restart), and a gateway-status footer.
3. **Events:** dense one-line rows (time · type · worker → target · summary) with
   a type click-through, text search, the **调用链** filter the talk view drops you
   into, and a payload inspector.
4. **Approvals:** the approver's queue (pending first, then decided history) with
   the request payload, 批准 / 拒绝 actions (`/api/approvals…`), and a header pill
   in 对话 when something is waiting.
5. **Workers:** the roster table (id · 备注名 · 类型 · 描述 · 连接 · 生命周期 · 托管)
   with create / archive / delete, and a detail panel offering 信息 / 自述 / 上下文 /
   事件 / 许可 — display-metadata editing, params editing, provider/model switch,
   `worker.readme`, context accounting, and the publish/subscribe allow lists.
6. **MCP 工作单元:** the MCP worker rail plus a panel that reads the live server
   list, lists a server's tools, switches the exposure mode, and adds/removes
   servers — all through the worker's own `mcp.*` events.
7. **程序:** the project's programs, with Markdown detail and create / edit /
   delete through the program worker.
8. **资源:** projects (create from template, link a directory, start/stop/
   restart), templates (JSON edit, create/clone, import .zip, delete), and model
   providers (visual read + JSON edit → `PUT /api/providers`).
9. **Composer:** recipient chips, real attachments (images inline as base64, other
   files uploaded to `/api/upload` and referenced by path), drag-and-drop, mode,
   stop, send.
10. **Polish:** 载入更早的消息 paging, a context-usage popover (grouped breakdown bar
    + legend), 外观 (system/light/dark) in the footer, and ⌘1–⌘9 view shortcuts.

**Style.** The WebUI's language is square: 1px borders, no rounded cards, small
monospaced caps for identifiers, one blue accent. The native client keeps that
for content and chrome (blocks, chips, tables, the composer panel) and uses
**native** controls only where the platform expectation is stronger — the
`.switch` toggles, `Menu`, `.popover`, sheets and `NSAlert`.

**Next (not implemented):**

- i18n (the WebUI's EN toggle); the native client is Chinese-only for now.
- A native @-mention popup inside the editor and scroll-to-quoted-message.
- The provider *visual* form (JSON editing covers the write path today), program
  sub-content editing, folder drops, worker “send event” form.
- Distribution: universal / zip validation, release notes, icon pass.

> **Reminder to the human (kept here so it isn't forgotten):** once this version
> is where you want it, install full Xcode and finish the *proper* setup — then
> migrate the state layer from `ObservableObject`/`@StateObject` to
> `@Observable`/`@State`, and add SwiftUI Previews (and optionally an Xcode
> project).
