# `app/publish/desktop` — the desktop distribution channel

The **app** lives with its source: [`app/client/desktop`](../../client/desktop).
This directory is the other half — how a release is produced and what it ships,
in the same spirit as `publish/cmd` (the CLI's goreleaser config) and
`publish/npm` (the npm package).

| Channel | Source | Ships |
|---|---|---|
| `publish/cmd` | `app/publish/cmd` | the CLI: goreleaser archives + `install.sh` |
| `publish/npm` | `app/publish/npm` | the `niq` npm package |
| **`publish/desktop`** | `app/client/desktop` | `niq.app` + `.dmg`, with the CLI bundled as a sidecar |
| `publish/releases` | — | per-version notes, `LATEST.md` |

## Platforms

| Platform | Runner | Artifacts | Notes |
|---|---|---|---|
| macOS | `macos-14` | `.dmg`, `.app.zip` | `--universal` (arm64 + x86_64 in one bundle); DMG needs a macOS runner |
| Windows | `windows-latest` | `.msi`, `-setup.exe` (NSIS) | x64; needs `icons/icon.ico` |
| Linux | `ubuntu-22.04` | `.AppImage`, `.deb`, `.rpm` | needs `libwebkit2gtk-4.1-dev`, `libappindicator3-dev`, `librsvg2-dev`, `patchelf` |

Each runner builds its own sidecar (`niq-<triple>`), so the app never depends on
a CLI being installed on the user's machine. macOS ships one universal bundle
instead of two per-arch ones to keep the download list short.

Windows and Linux are wired to Tauri's documented bundle layout but have only
been exercised on macOS locally; the first tag run is their real test.

## What a release is

```
app/client/desktop (Tauri crate + ui/)          ← the app
        + binaries/niq-<triple>                 ← the CLI, staged by the build
        + tauri.conf.json version               ← stamped here
        ↓
dist/niq-desktop_<version>_<arch>.dmg
dist/niq-desktop_<version>_<arch>.app.zip
```

The app carries the CLI, so a user installs **one** thing and gets a working niq:
the shell starts the sidecar, waits for the gateway, and loads the WebUI.

## Cut a release

```sh
make dist-desktop                        # host build → dist/
make dist-desktop UNIVERSAL=1            # arm64 + x86_64
./app/publish/desktop/release.sh --version 0.18.0    # stamp, then build
```

`release.sh --help` lists the flags. The script:

1. optionally stamps the version into `tauri.conf.json` + `Cargo.toml`;
2. stages the sidecar (`binaries/niq-<triple>`, or a lipo'd
   `niq-universal-apple-darwin` for `--universal`);
3. runs `tauri build`;
4. signs, and notarises + staples when credentials are present;
5. copies the `.dmg` and a zipped `.app` into `dist/` with stable names.

## CI

`.github/workflows/release.yml` runs three jobs on a tag:

1. **release** — goreleaser builds the CLI and creates the GitHub Release;
2. **desktop** — a matrix over macOS / Windows / Linux, each running
   `app/publish/desktop/release.sh --version ${tag}` and attaching its artifacts
   to that release with `gh release upload`. `fail-fast: false`, so one platform
   failing does not stop the others; re-run the failed job from the Actions tab.
3. **npm** — publishes the packages from the CLI archives.

Secrets the desktop job uses (all optional, see below):
`APPLE_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_PASSWORD`, `APPLE_TEAM_ID`.
Goreleaser creates the release first, so `gh release upload` has somewhere to put
the files; the desktop job therefore depends on it.

The build logic is a shell script rather than inline workflow steps precisely so
`make dist-desktop` and CI produce the same artifacts.

## Signing and notarisation

| Env | Effect |
|---|---|
| `APPLE_SIGNING_IDENTITY` | Tauri signs the bundle (hardened runtime) with this identity |
| `APPLE_ID` + `APPLE_PASSWORD` + `APPLE_TEAM_ID` | the `.dmg` is submitted to `notarytool` and stapled |

Without them the build is **ad-hoc signed**: it runs on the machine that built it,
and Gatekeeper will refuse it elsewhere. A public release needs both, plus the
notary credentials in CI secrets.

## Not done yet (deliberate)

- **A PR-time build check.** The desktop app is currently built only at release
  time, so a break shows up on a tag rather than on the pull request. A
  path-filtered job (build on changes under `app/client/desktop`) would fix that.
- **Universal by default.** `--universal` works but is not the default; it needs
  both rust targets and doubles the build time.
- **Auto-update.** Tauri's updater plugin would give signed in-app updates; it
  needs an update endpoint and a signing key, so it is a separate decision.
- **Release notes hook.** `publish/releases/vX.md` is written by hand today; the
  script could template it from the git log between tags.
