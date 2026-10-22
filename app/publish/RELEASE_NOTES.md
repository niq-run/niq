# niq v0.9.0

This release lands **PTC (script programs)**, a **coding worker**, a unified
**reply / capability contract**, and **TypeScript SDK 0.5 / 0.6**, plus a round
of WebUI polish. The main change for everyone integrating with niq is the reply
contract: a structured reply is now the payload itself, and every built-in
worker advertises its reply shape in `worker.ready`.

## Highlights

- **Program worker: PTC / script programs.** `program.run` executes a stored
  script program (`path`) or inline source (`source`) in a local subprocess,
  relaying every `sdk.call` as a normal bus request and returning a structured
  `{ output, log?, log_truncated? }` reply.
- **New coding worker** (`type: "coding"`) with `normal` and `ptc` tool modes.
  In PTC mode the model sees only its own tools plus directory (discovery) and
  program (invocation); peer capabilities are reached through a script.
- **Reply contract.** An object reply *is* the payload; non-object replies
  (string / number / array) travel under `result`. Structured results keep
  their type through the whole path — no JSON-string wrapping anywhere.
- **Declared reply shapes.** `Extension.Response` is carried in `worker.ready`'s
  `watch`, so `directory.get_worker_info` and PTC scripts can see what a
  capability returns before calling it.
- **TypeScript SDK 0.5 / 0.6** keeps external workers in step: schema builders
  and `response`, then automatic `worker.discover` answers with transient
  presence.

## Program worker — PTC (script programs)

- `program.ProgramBackend` (= `Store` + `Executor`) makes "where content lives"
  and "how a script runs" one swappable environment; `core/itfs/program`
  provides `ScriptSession` / `ToolCaller`.
- `program.run` accepts exactly one of:
  - `path` — run a stored script program (its entry content is the source;
    `name` remains as a deprecated alias), or
  - `source` — run inline JavaScript, nothing persisted.
- Optional `args` (exposed as `sdk.args`) and `timeout_seconds` (default 120s
  for the whole run, including nested calls; the process is killed on expiry).
- The reply is an object: `output` keeps the script's return type, `log` carries
  the `sdk.log` lines (tail-capped, with `log_truncated` when older lines were
  dropped). Results over 20 KiB are head/tail truncated.
- The executor (`program/jsexec`) spawns a local subprocess and runs an embedded
  `launcher.mjs` that compiles the script in memory and injects a minimal SDK
  (`call / log / result / args`) over JSONL on stdout. **node is the default
  runtime**; `script_executor_args` can point it at `bun run` or a wrapper.
  `install.sh` now warns when node is missing.
- Script tool calls are relayed as ordinary requests, matched back by request
  id: the script receives the reply **value** (objects stay objects, no
  `JSON.parse`). A target is validated through the directory worker first, and
  an empty target is rejected. A tool-level failure is an ordinary result; only
  an unknown worker / `request.failed` / `request.rejected` rejects.

## Coding worker

- New package `core/impl/workers/coding`, parallel to the generic reason worker;
  it announces `type: "coding"`.
- `tool_mode`:
  - `normal` — the full discovered tool list, like a generic reason worker;
  - `ptc` — keeps its own tools plus directory (capability discovery) and
    program (capability invocation); every other peer tool is removed from the
    model's tool list and the self `request` escape hatch is dropped, so peer
    calls go through `program.run`. Seeds the locked `coding-ptc-tooling`
    instruction.
- Both modes drop `context.compress` and `program.update` (program content is
  fixed at creation).
- New reason seams: `reason.Config.ToolListBuilder`, exported
  `DefaultToolListBuilder` / `ToolNameOfCapability` /
  `RegisterDefaultExtensions`; `build.go` shares reason-family parsing and adds
  the coding builder (`tool_mode` / `program_worker` / `directory_worker`).

## Reply & capability contract

- `core/itfs/event/reply.go` is the single place that encodes the rule:
  - an object reply is the payload itself;
  - a string / number / array reply is wrapped as `{ result: value }`;
  - `result` and `content` stay reserved (the latter for multi-modal blocks),
    so a structured reply must not use them as top-level field names.
- `Extension.Response` declares the reply's JSON Schema. It rides
  `worker.ready`'s `watch` next to `parameters`, so peers, the directory roster
  and PTC scripts can plan their parse up front. Every built-in worker now
  declares it (directory, history, host, mcp, niw, program, reason, timer,
  workspace, and `worker.readme`).

## TypeScript SDK (`@niq.run/worker-sdk`)

- **0.5.0** — aligned with the reply payload rule; `Extension.response`;
  `s.*` schema builders; accepts a schema library's schema through
  `toJSONSchema` / `toJsonSchema` with no added dependency; `SchemaSource` is
  object-typed; the published tarball excludes tests.
- **0.6.0** — `announceReady` remembers the announced contract and marks
  presence transient (matching Go); adds `announceReadyTo(target)`;
  `dispatchExtension` answers `worker.discover` automatically with a directed,
  transient `worker.ready`. A worker that calls `announceReady` and uses
  `dispatchExtension` gets discovery for free.

## reason / directory / mcp

- **reason**: new `message` and `request` self tools; `worker.readme` (a
  self-description built from live state, seedable and updatable at runtime);
  fixed a self-deadlock on `worker.readme`.
- **directory**: keyword search (`search_workers`) and a minimal roster
  (`list_workers`); history / timer / directory gained `worker.readme`.
- **mcp**: tool allowlist, readme overrides, host-snapshot persistence, and
  ready mode driven by extensions.

## WebUI

- **Worker-type tags collapsed to four groups** — built-in / `reason` /
  `coding` / external — with a low-saturation palette and ≥ 4.8:1 contrast in
  both themes. Unknown types are no longer gray.
- Response copy (context menu and a bottom-left button); the quoted response no
  longer reflows after a reply settles.
- Sidebar worker selector: right-click for details, a pinned marker, stable
  pinned ordering with drag-to-reorder, and drag ordering for every row
  (persisted in `localStorage`).
- Worker list defaults to online only and drops the tags column; the coding
  worker's create form is complete; coding is a talk partner; reason-family
  recognition is unified.
- The event stream reconnects itself after the browser gives up; worker params
  are editable with managed restart; mcp fixed toggle, built-in singletons
  hidden, create-dialog actions pinned.

## Upgrade notes

- **External TypeScript workers**: upgrade to `@niq.run/worker-sdk` ≥ 0.5.0 to
  advertise `response`; ≥ 0.6.0 to inherit `worker.discover` handling and
  transient presence. A worker that builds `worker.ready` by hand should mark
  it `transient` and answer `worker.discover` with a directed ready.
- **Structured replies**: return the object itself — do not JSON-encode it, and
  do not use `result` / `content` as top-level fields.
- **Script programs** need a JS runtime on the host. node is the default;
  `script_executor_args` overrides it (e.g. `["bun", "run"]`).

## Validation

- `go build ./...`, `go vet ./core/... ./app/...` clean.
- `go test ./core/... ./app/...` — all packages pass.
- `npm test` in `sdk/ts` — 48 tests pass.
- WebUI bundle rebuilt (`npm run build`) and embedded (`go build ./app/webui`).
- Release binaries for darwin / linux / windows on amd64 + arm64 built by the
  release workflow, with `niq --version` reporting `0.9.0`.
