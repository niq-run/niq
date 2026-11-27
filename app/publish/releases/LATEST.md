# niq v0.19.0

A release about the thing you install: it now looks and behaves like the menu bar
app it claims to be, and the pipeline that builds it is checked before a tag
instead of after one.

v0.18.0 shipped the app, but every defect it had surfaced at tag time. One
release went out with no macOS bundle, because the universal build asked for
sidecar names that did not exist. The next one shipped bundles whose ad-hoc
signature did not verify, so a copy handed to someone else read as "damaged".
Neither could have been caught: nothing built the shell before a tag, and the
build script ignored its own signing failures.

Both are fixed here, and both are now guarded: CI builds the shell on all three
platforms, and a bundle that does not verify stops the build.

## Highlights

- **The bundles verify.** Signing runs from the inside out — the CLI sidecar
  first, the bundle last — because `codesign` refuses a bundle while a
  subcomponent is unsigned. The bundle is then *checked*, and a bundle that fails
  the check is not an artifact: the build stops.
- **CI builds the shell on macOS, Windows and Linux**, on the paths that can break
  it. Windows and Linux had never been built outside a tag.
- **Closing the window puts it away.** The shell is a gateway client that lives in
  the menu bar, so close hides the window and keeps the process — and the WebUI's
  state — alive. ⌘Q or 退出 quits; the Dock icon brings the same window back.
- **A menu bar mark that survives 18 pt.** Flat and one colour: three squares in
  the 品 arrangement. The shaded cubes turned into noise at that size, and a
  framed variant took the space the squares needed.
- **The app icon's right face is `#666666`**, up from `#4D4D4D`, which read as
  black against the tile — the cubes lost their third face.
- **A worker can patch its own declaration and keep durable state** in the
  TypeScript SDK: `patchConfig` hands the patch to whoever owns the declaration,
  and `loadState`/`saveState` sit on a `DurableStore` that only moves text.
- **The npm package stopped claiming what it does not carry.** `miq` was a `bin`
  entry for a file nothing creates, and `files` listed an `niq.app/` that no
  longer ships. Both are gone, and the install page now points at the app's own
  channel instead of promising npm would deliver it.

## 1. The release you install

`release.sh` signed the bundle without looking at the result. A nested binary that
could not be signed, or a bundle that failed `codesign -v`, printed a line and the
build carried on — which is exactly how a "damaged app" gets published. Signing
now goes from the inside out and every step is fatal on failure:

```sh
for bin in "$app/Contents/MacOS"/*; do codesign --force --sign - "$bin"; done
codesign --force --sign - "$app"
codesign -v "$app"   # or: refuse to ship
```

The universal build also stages one sidecar per target triple now, which is the
fix for the release that shipped without a macOS bundle at all: it had asked for
`binaries/niq-universal-apple-darwin` while the build produced per-arch names.

## 2. The shell behaves like a menu bar app

Closing the window no longer quits. `CloseRequested` is intercepted and turned
into a hide, so the process keeps running and the WebUI keeps its scroll position
and panels; `RunEvent::Reopen` brings that same window back from the Dock icon.
Quitting stays explicit: ⌘Q, or 退出 in the menu bar item.

The menu bar mark is new: flat, single colour, three squares in the 品
arrangement, drawn at 81% of the 18 pt slot so it sits with the icons beside it.
The app icon's right face went from `#4D4D4D` to `#666666` for the same reason —
at tile size the darker face disappeared.

## 3. The TypeScript SDK

- **`patchConfig`** forwards a merge patch to a provider the caller installs with
  `setConfigPatcher()`. With no provider it throws `ConfigUnavailableError`
  instead of writing something itself: a worker never edits its own declaration,
  and ownership, locking and validation stay in the layer that composes it.
- **`loadState` / `saveState`** speak JSON over a `DurableStore`, which only moves
  text — serialization policy stays the worker's, and a database row or remote KV
  fits as well as a file. `FileDurableStore` replaces `state.json` through a
  temporary sibling, and 0.11.1 makes it apply saves in call order. With no store
  configured, the state calls throw `StateUnavailableError` rather than silently
  doing nothing.

Published as `@niq.run/worker-sdk` 0.11.2.

## 4. CI, and what a failed release means now

The shell is built in CI on macOS (universal, with the signature and `lipo`
assertions), Windows and Linux, path-gated so an unrelated PR pays nothing.
Workflows now run on pull requests and on `main`; merging into `dev` no longer
re-runs the commit that was just validated.

The manual "rebuild the desktop artifacts for an existing tag" workflow is gone.
A published version is immutable — npm will not accept a re-publish, and re-running
a tag's job replays the workflow as it was at the tag, so a fix cannot take effect
there. A failed release is fixed by cutting the next patch version, which rebuilds
the CLI, the three bundles and the npm packages together from the fixed script.

## 5. Docs that match

`build-and-test` was written a week ago and had already drifted: it named the Swift
menu bar app as the extra toolchain, claimed `make build` built the app "only on
macOS", and carried stale counts. The counts are measured now (Go 147 test files /
867 tests, SDK 99 tests). `install-and-cli` no longer documents conveniences the
npm package does not provide, and `how-to-build-a-client` points at the in-repo
Tauri shell rather than the macOS client, which lives in the clients repository.
