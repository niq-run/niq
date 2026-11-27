# niq v0.18.0

A release about who holds what: the thing you install, the token you paste, and
the shape of the call that starts a worker.

Three of them were being held in the wrong place. The app existed as a menu bar
item while the real interface was a browser tab, so installing niq meant
installing two things. A credential lived inside a project's `workers.json`,
where it could not be reused by the project next door. And a construction
argument could arrive in two shapes — flat next to `type`, or nested under
`params` — which meant the tool had to guess, and a caller that guessed wrong got
a worker that ignored what it was told.

So: **one installable app, one credential store for the machine, one shape for
`spawn`.**

## Highlights

- **The release now ships an app, not just a CLI.** `niq-desktop` for macOS,
  Windows and Linux: a webview that loads the gateway's own WebUI, with the CLI
  bundled as a sidecar. One download, one thing to install.
- **`app/standalone/vault`** replaces `secret`. A credential lives in one store
  the whole machine shares, and a project only references it by name. A literal
  is sealed into the vault on write, so `workers.json` holds a reference.
- **`spawn` takes its type-specific arguments in `params`** and nothing else. A
  flat argument is now an error that names it, instead of being merged in.
- **A type declares what is secret** (`ParamSpec{Name, Secret}`), so a missed
  call can no longer write a token to disk.
- **A worker can patch its own declaration** — `PatchConfig` is an internal
  method with the writer injected by the assembly, so ownership, locking and
  validation stay in the control plane.
- **Context usage is reported as a breakdown** of the exact request that was
  sent, and the cache hit rate is finally computed on a normalized prompt count.
- **The terminal client is a conversation**: `niq c`, scrolling, selection,
  model switching, a command palette, and a package split to hold it.
- **CI exists.** Unit tests with `-race`, the black-box suites, cross-compiles,
  and the two Node trees — on every pull request, not at tag time.

## 1. The app you install

`app/client/desktop` is a Tauri v2 shell whose UI *is* the WebUI. It bundles the
CLI as a sidecar (`bundle.externalBin` + `tauri-plugin-shell`), starts the
gateway with `--local`, waits for `/api/projects`, and loads it in the webview.
The UI is maintained once, in `app/client/webui`, and the desktop shell is a
different way to reach it — the same relationship the TUI and the WebUI already
had.

- **Gateway discovery** is a list, not a guess: `NIQ_GATEWAY_URL` →
  `~/.niq/shell.json` → `niq status --json` → `127.0.0.1:9527`. Start, probe and
  discovery are stdlib-only Rust.
- **A bundle-local splash** using the WebUI's own logo font hands off through a
  `ui_ready` command and offers a retry when the gateway never answers.
- **The WebUI is a remote origin.** Tauri treats `http://127.0.0.1:9527` as
  remote, where *every* command needs an explicit ACL entry — including plugin
  ones. `capabilities/webui.json` therefore grants exactly `notification:default`
  to loopback origins and nothing else: no shell, no sidecar.
- **Notifications work.** The plugin posts real system notifications but drops
  the click, so the WebUI's own handler never ran. `src/notify.js` runs after the
  plugin's shim (a webview's initialization scripts are appended after the
  plugins'), hands the plugin an integer id that comes back in the `tap` action,
  and keeps the page's handler. The WebUI was not touched; it is written against
  the standard `Notification` API, and that is the whole point.
- **A menu bar item** as a template image, so macOS tints it with the menu bar
  and it reads at 18 pt. The window is built in code rather than declared in
  `tauri.conf.json`, because only a webview's own initialization script runs
  after the plugins'.

**Building and installing.** `make build` includes the shell whenever Rust and
the Tauri CLI are present (and skips it quietly otherwise); `make install` copies
`niq.app` into `/Applications` or `$HOME/Applications` next to the usual CLI
symlink; `make desktop-sidecar` stages the binary. Both paths resolve
`cargo`/`tauri` past `PATH`, because the npm Tauri CLI shells out to
`cargo metadata`, which silently fails from a non-interactive shell.

**Signing is the part users notice.** Tauri leaves the app with only the
linker's signature on the Mach-O, which is not a bundle signature — `codesign -v`
rejects it, and macOS reports the app as damaged. `release.sh` now signs the
bundle itself when there is no `APPLE_SIGNING_IDENTITY`, which seals `Resources`
and covers the `.dmg` and the `.zip` alike. Quarantine is the other half: an
ad-hoc signature can never satisfy notarisation, so a downloaded copy is still
refused — just with the ordinary "unidentified developer" block. Ad-hoc builds
therefore ship `INSTALL.txt`, inside the DMG and beside it in `dist/`, with the
one `xattr` command that clears it. Notarised builds ship neither.

**The release pipeline attaches the installers.** A tag now produces
`niq-desktop_<version>_macos_<arch>.dmg`, `…_windows_<arch>.msi` / `-setup.exe`
and `…_linux_<arch>.AppImage` / `.deb` / `.rpm` alongside the CLI archives, from
one job per platform (`fail-fast: false`, so a platform that fails does not take
the release down — re-run that job). macOS builds one universal bundle. The
version stamp is pure `sed`, because the Windows runner's Git Bash does not
guarantee a `python3`.

Worth saying plainly: **macOS has been built and run locally; Windows and Linux
are wired to Tauri's documented bundle layout and this is their first real
test.** Treat a first-run problem there as a build-plumbing issue, not a
regression in the app.

**The native macOS client left the repository.** The SwiftUI client that
mirrored the WebUI now lives in `github.com/niq-run/clients`, along with the
other third-party clients; `app/client/mac` and `app/publish/mac` are gone. What
stays here is the core and its own interfaces: CLI, WebUI, TUI and the one
installable shell.

## 2. One vault for credentials

Credentials used to live in project files, which meant a token was per-project,
even when it was the same token.

- `app/standalone/vault` replaces `app/standalone/secret`. One store at
  `<root>/common/vault`, next to `providers/`, shared by the whole machine. A
  project references an entry by name — which is exactly what makes a credential
  reusable.
- One file per entry, mode `0600`. Names are flat and may carry slashes for
  grouping, but that is a view, not a hierarchy: there are no per-scope
  permissions.
- `Resolve` understands `vault:` / `env:` / `file:` / `literal`; `Store` writes
  `vault:` and `file:`. Values are plaintext at rest for now, behind an API a
  keychain or database backend can replace without touching callers.

**The write path seals literals.** A type declares which of its params carry a
credential (§4), and the control plane — the single writer of `workers.json` —
puts a literal into the vault *before* writing the declaration, keeping only
`vault:<project>/<worker>/<param>`. An existing reference is never re-sealed (so
re-saving cannot overwrite a deliberate choice), a non-string value is an error,
and destroying a worker deletes nothing, because the credential may be shared.
Where no vault is wired (embedded hosts), values are left untouched: sealing
needs somewhere to put them.

`provider.json` goes through the same resolver, so `api_key` and header values
may be references; `${VAR}` expansion is kept for compatibility.

**The WebUI gets a Credentials panel**, and it is metadata-only by construction:
`GET /api/vault` never returns a value, and `PUT`/`DELETE /api/vault/{name...}`
are the only writes. The panel lists entries as "set" and offers replace or
delete, so the browser never holds a token it did not just type.

## 3. `spawn` takes one shape (breaking)

`spawn` accepted construction arguments two ways at once: flat, next to `type`
(what an agent sends), and nested under `params` (what the create form and the
declaration use), with a flattening step merging the latter into the former. Two
shapes for one concept forced the tool to carry a collision guard — a param
named `id` or `placement` would otherwise change how the worker runs — and a
caller that guessed wrong still got a successful-looking spawn that had silently
dropped its arguments.

The nested bag is the shape everything else already speaks: a declaration stores
`params`, the update tool edits `params`, the WebUI create form posts `params`.
So `spawn` now reads `params` and nothing else. After the control fields
(`type` / `id` / `placement` / `command` / `cwd` / `label` / `tags` /
`description`) are lifted, any leftover key is rejected by an error that names
it and points at `params`; a non-object `params` is an error too. A key *inside*
`params` is just a parameter, so it can never shadow a control field — which
retires the collision guard rather than keeping it.

The schema no longer enumerates type-specific properties, so it cannot drift
from the per-type contracts again; the dead `token_ttl_seconds` property (TTLs
are constants) and the unused `WorkerSpawnArgs` interface are gone. The
agent-facing text follows: the help reference, `worker.readme` and the using-niq
playbook all say where arguments belong.

## 4. A declaration says what is secret, and a worker may patch it

- **Secrecy is part of the contract.** A type used to declare its accepted
  params as `[]string` and separately call `MarkSecret` for the one that holds a
  credential — two places to keep in sync, and a missed call silently wrote a
  token into `workers.json`. `RegisterType` now takes
  `[]builtin.ParamSpec{Name, Secret}`: one declaration yields both the accepted
  set and the secret set, so sealing is derived, not remembered. `Specs(typ)`
  exposes the whole registration for tests; `MarkSecret` remains the
  `RegisterBuilder` seam for out-of-tree types.
- **A worker can persist a change to its own declaration.**
  `baseworker.PatchConfig` is an internal method, not a bus tool: the write is
  injected by the assembly (`iface.Config.PatchConfig` → `SetConfigPatcher`) and
  implemented by the control plane, so ownership, validation, per-id locking and
  the "applies on the next start" contract stay in one place. Without an
  injected writer it returns `ErrConfigUnavailable`, so a caller can degrade
  deliberately.
- **`update` merges instead of replacing.** Only the given keys change, a `nil`
  value clears one, an empty map is a no-op, and validation runs on the merged
  result — naming one field can no longer clobber the rest.
- **`niq.dial` uses all of it**: a repoint is persisted *before* it is applied,
  as a merge patch of only the keys the call changed, and a failed write leaves
  the old target in force. This replaces a direct manifest write that bypassed
  the control plane.
- **Secrets became an assembly concern.** The env/file provider is a dependency
  of the code that *builds* workers, not a core contract: workers never see it,
  `niq` receives a plain resolver func (a runtime dial can carry a new
  credential), and the same instance is handed to the control plane for
  managed-process env expansion. `process.ResolveSecret`, now unused, was removed
  rather than left as a second path.

## 5. Context accounting

- **`meta.usage` carries a `breakdown`**: the system prompt, the applied
  programs, the tool definitions (grouped by the worker offering them), the
  transcript's message kinds and this round's output. Each part is estimated from
  the exact request that was sent and rescaled to the provider's reported input
  count, so the parts add up to the headline the UI already showed.
- **Two normalization fixes**, because a cache hit rate is only meaningful on a
  normalized prompt count: Anthropic reports cache tokens outside
  `input_tokens`, and the adapter now sums them into `InputTokens` (budget
  accounting and occupancy had been undercounting well-cached prompts); the
  OpenAI adapter reads DeepSeek's top-level `prompt_cache_hit_tokens` as well as
  `prompt_tokens_details.cached_tokens`, and distinguishes "reported as 0" from
  "not reported", so a cold cache reads 0% instead of rendering nothing.
- **The message-side estimate moved** into `transcript.EstimateByKind`, beside
  the estimator compaction budgets already use; its kinds partition exactly the
  same total, which a test now pins.
- **The reason worker reports its accounting as state** — a `context.status`
  event, persisted in its snapshot. The WebUI serves it as
  `GET /api/workers/{id}/context` (a directed call, so no subscription grant is
  involved) and shows it in a **Context tab** on the worker detail, which now
  also hosts the model/provider switcher: "what am I running on" and "how full is
  it" are the same question. Both right-click menus deep-link to it, offered only
  for reason-family workers, with a fallback to Info when a worker has no such
  tab.
- **Provenance survives a provider switch.** The reason worker stamps each
  assistant message with the model that produced it and reports the same value
  in `meta.usage`, so a stored transcript keeps its history. The usage label
  shows it — and stopped being cut off: it no longer relies on `text-overflow`
  in a flex container, wraps its two halves when they cannot share a line, and
  ellipsizes only when a half alone is wider than the row.

## 6. The terminal client is a conversation

`niq c [project]`, or `niq i` then Chat, opens a live view of one project's bus:
it sends as the human's interface worker and prints events as they arrive. The
wire is the gateway's existing project API — the TUI is just another client, and
the stdlib-only / client-imports-only-`contract` rules still hold.

- **Rendering separates the conversation from the process.** Human turns stay
  inline and underlined; a worker's answer renders markdown — headings,
  emphasis, lists, quotes, pipe tables fitted to the width, fenced code with
  light highlighting. Everything else (thinking, tool calls, results) collapses
  to one coloured `label: detail` line, truncated rather than wrapped, so a long
  thinking trace cannot push the conversation off screen.
- **Scrolling and selection** go through one viewport that owns content length,
  visible height, first line and follow state, so paging behaves identically
  however it was triggered (keys, wheel, drag past an edge, history load).
  vim-style keys, and a selection component that maps cell ranges to clipboard
  text via OSC 52 plus a best-effort `pbcopy`/`wl-copy`/`xclip`/`xsel`.
- **A model switcher and context footer**: each `reason.response` trails a dim
  usage line, a line under the composer shows the bound worker's occupancy and
  model, and the model is a click target that switches the worker through
  `WorkerProviders` / `SetWorkerProvider`.
- **A slash-command palette**: typing `/` opens the command list under the
  composer and filters live; Tab or Enter commits the row.
- **A package split**: `term/`, `screen/`, `keys/`, `layout/`, `render/`, `ui/`
  and `picker/`, with the chat view itself in the root package. Also fixed: BMP
  emoji width (U+2705, U+2B50, …) so banded cards and the modal overlay stop
  drifting a column; `Ctrl+N`/`Ctrl+P` jump between user messages; `Cmd`/
  `Ctrl`+`Backspace` deletes to the start of the line.

## 7. The worker type table is its own package

`worker` held three concerns at once — the control plane, the manifest model and
the built-in type table. The third is assembly knowledge, not mechanism: the
control plane knows a type only as a label, `workerctl` knows nothing at all, and
a different deployment form would want a different table.

It moved to `app/standalone/worker/builtin`, reorganised the way it is used: one
file per type, holding that type's accepted parameters, its builder and its
registration together (`builtin/workers`). `SpawnParamsByType` — one map in a
file far from the builders that had to agree with it — is gone; each type owns
its list, and `RegisterType` turns it into the check in the same call. The
coverage test now walks the registry in both directions instead of comparing two
globals. `worker` keeps three files: the control plane, the backend (placement
routing, lifecycle) and the manifest model with its store. `SanitizeID` became
`helper.SafeName`, one rule for every id that becomes a directory name, and
`EnsureIdentity` is exported because it is the registration half of a builder's
connect path.

## 8. CI

Until now the only workflow was the release one, so a pull request could merge
with a red suite and the first place a break showed up was a tag push. Five jobs
now answer "does this still work", each reproducible by one command:

| job | runs | why it exists |
|---|---|---|
| `go` | build, vet, `go test -race ./...` on ubuntu + macOS | the bus is a concurrent event system where a race kills a process instead of failing an assertion |
| `e2e` | `make e2e` on both platforms | the only tests that observe the process boundary; signals, port holders and `open` vs `xdg-open` are platform-specific |
| `cross` | the windows, linux/arm64 and darwin targets the release builds | a broken `_windows.go` is invisible on the runners above and would surface at tag time |
| `sdk` | the TypeScript SDK's tests | published on demand, versioned separately |
| `webui` | that the committed, `go:embed`-ed bundle is what `vite build` produces | a stale bundle leaves `go build ./...` green while the binary serves the old page |

## 9. Also in this release

- **The CLI asks about exposure once.** The control plane binds loopback, which
  is right, but a user who needed remote access could not discover that from the
  start command. The address is resolved before the daemon is spawned (a
  detached child has no stdin): explicit `--addr` / `--remote` / `--local`, then
  the preference remembered in `common/config.json`, then loopback. On a terminal
  with nothing decided, a start asks — and offers to remember. Exposing stays
  behind auth: `--remote` fails with the fix in its message until credentials
  exist, and a remembered non-loopback address falls back to loopback with a note
  rather than starting unauthenticated. `status` names the exposure and, when
  reachable, prints the URL another machine would open.
- **`stop` waits for the process**, not just for its port to close.
- **Interface worker rename**: `@niq.run/agent-interface-worker` → `@niq.run/interface-worker`, bin `niq-agent` → `niq-interface`, credential file `niq-interface-<id>.json`. The JSON format is unchanged. The TypeScript SDK gains the stateless half (`InterfaceSession`: connect, send/broadcast, request with reply correlation and timeout, events, close) at 0.10.0.
- **A worker id fix**: the human's worker is `webui`, not `webui-hiw`.
- **Docs**: a new `doc/how-to-build-a-client` (English + zh-CN) for anyone writing
  their own client against the gateway, a README for every package under
  `app/standalone`, an architecture diagram of the worker subsystem's shape, and
  `build-and-test` corrected — it had said there was no CI job and no end-to-end
  suite, both true when it was written.

## Upgrading

Data files keep their names and the schema stays at 2, so projects and workers
load in place.

- **`spawn` no longer accepts flat type-specific arguments.** Anything that built
  a `spawn` call by hand must move those keys into `params`. The WebUI already
  did; an agent is told by the error message.
- **Existing literal secrets are not migrated.** Sealing happens on the write
  path, so a credential already sitting in a `workers.json` stays there until
  that declaration is written again (a re-save or an update). Moving it now is
  one `update` call with the same value; it lands in the vault as
  `vault:<project>/<worker>/<param>`.
- **The desktop app is a new download.** Ad-hoc signed builds (no Developer ID in
  the release secrets) need the `xattr` line from `INSTALL.txt`; notarised builds
  do not.
- **The native macOS client moved** to `github.com/niq-run/clients`. If you ran
  the SwiftUI app from this repository, it is not in this release.
- **The npm interface worker was renamed** (`niq-agent` → `niq-interface`); a
  connect config generated by an older WebUI points at the old binary name.
- **Go import paths** for the worker type table moved with the package
  (`app/standalone/worker` → `app/standalone/worker/builtin` for the table
  itself). Nothing outside the repository is expected to import them.
