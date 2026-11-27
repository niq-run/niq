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
| macOS | `macos-latest` | `.dmg`, `.app.zip` | `--universal` (arm64 + x86_64 in one bundle); DMG needs a macOS runner |
| Windows | `windows-latest` | `.msi`, `-setup.exe` (NSIS) | x64; needs `icons/icon.ico` |
| Linux | `ubuntu-22.04` | `.AppImage`, `.deb`, `.rpm` | needs `libwebkit2gtk-4.1-dev`, `libappindicator3-dev`, `librsvg2-dev`, `patchelf` |

Each runner builds its own sidecar (`niq-<triple>`), so the app never depends on
a CLI being installed on the user's machine. macOS ships one universal bundle
instead of two per-arch ones to keep the download list short.

The macOS runner is `macos-latest`, not a pinned image: `macos-14` resolves to a
runner configuration this repository is not entitled to, and the job is cancelled
before its first step with "The configuration 'macos-14-arm64-us-default' is not
supported". `macos-latest` is what the CI workflow already uses.

All three platforms ship for v0.18.0, but not all three built on the tag: Windows
and Linux produced their bundles there, while macOS was rejected by the runner
before its first step and was built locally with `make dist-desktop UNIVERSAL=1`
and attached by hand. That gap is why the shell now also builds in CI.

## What a release is

```
app/client/desktop (Tauri crate + ui/)          ← the app
        + binaries/niq-<triple>                 ← the CLI, staged by the build
        + tauri.conf.json version               ← stamped here
        ↓
dist/niq-desktop_<version>_<arch>.dmg
dist/niq-desktop_<version>_<arch>.app.zip
dist/niq-desktop_<version>_<arch>.INSTALL.txt   ← ad-hoc builds only
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
2. stages the sidecars (`binaries/niq-<triple>`; for `--universal`, both
   architectures plus the lipo'd `niq-universal-apple-darwin` that the bundling
   step copies in);
3. runs `tauri build`;
4. signs, and notarises + staples when credentials are present;
5. copies the `.dmg` and a zipped `.app` into `dist/` with stable names.

## CI

`.github/workflows/release.yml` runs three jobs on a tag:

1. **release** — goreleaser builds the CLI and creates the GitHub Release;
2. **desktop** — a matrix over macOS / Windows / Linux, each running
   `app/publish/desktop/release.sh --version ${tag}` and attaching its artifacts
   to that release with `gh release upload`. `fail-fast: false`, so one platform
   failing does not stop the others.
3. **npm** — publishes the packages from the CLI archives.

Secrets the desktop job uses (all optional, see below):
`APPLE_SIGNING_IDENTITY`, `APPLE_ID`, `APPLE_PASSWORD`, `APPLE_TEAM_ID`.
Goreleaser creates the release first, so `gh release upload` has somewhere to put
the files; the desktop job therefore depends on it.

The build logic is a shell script rather than inline workflow steps precisely so
`make dist-desktop` and CI produce the same artifacts. `.github/workflows/ci.yml`
also builds the shell — on all three platforms, on the paths that can break it —
so a broken script is a red pull request rather than a broken tag.

## When a desktop job fails on a tag

Cut the next patch release. A version is immutable once published: npm will not
accept a re-publish of the same version, and re-running the failed job from the
Actions tab replays the workflow **as it was at the tag**, so a fix to
`release.sh`, to a runner label or to the toolchain cannot take effect there.

```sh
# fix on dev, land it, then:
git tag v0.18.1 && git push origin v0.18.1
```

The tag rebuilds everything — CLI archives, the three desktop bundles, the npm
packages — from the fixed script, which is the point: a release is one thing,
not a set of artifacts to be patched individually.

If the broken release should not stay visible at all, delete it before tagging:
`gh release delete v0.18.0` (the tag itself can be removed with `git push
--delete origin v0.18.0`). The npm packages matter more: those versions are
consumed already, so prefer a new patch version over trying to withdraw them.

## Signing and notarisation

| Env | Effect |
|---|---|
| `APPLE_SIGNING_IDENTITY` | Tauri signs the bundle (hardened runtime) with this identity |
| `APPLE_ID` + `APPLE_PASSWORD` + `APPLE_TEAM_ID` | the `.dmg` is submitted to `notarytool` and stapled |

Without them the build is **ad-hoc signed**: it runs on the machine that built it,
and Gatekeeper will refuse it elsewhere. A public release needs both, plus the
notary credentials in CI secrets.

## Why a shared copy says it is "damaged"

Two different things produce that message, and only one of them is quarantine.

**A broken bundle signature.** Tauri leaves the app with just the linker's
signature on the Mach-O — `codesign -dv` shows `flags=0x20002(adhoc,linker-signed)`
and `codesign -v` fails with *"code has no resources but signature indicates they
must be present"*. A bundle whose signature does not verify is exactly what macOS
calls damaged. `release.sh` therefore signs the bundle itself when there is no
`APPLE_SIGNING_IDENTITY`, which seals `Resources` and makes `codesign -v` pass
(`flags=0x2(adhoc)`, `Sealed Resources version=2`). Both the `.dmg` and the
`.app.zip` are built from that signed bundle.

**Quarantine with no notarisation ticket.** Anything downloaded carries
`com.apple.quarantine`, and an ad-hoc signature can never satisfy the
notarisation requirement. Once the signature verifies, this is the ordinary
"unidentified developer" block rather than a damaged app.

**Signing it yourself does not help.** A self-signed certificate is trusted only
on machines where that certificate is installed and trusted; the recipient's
Gatekeeper still refuses the bundle. Two things actually work:

| Path | Effect |
|---|---|
| `xattr -dr com.apple.quarantine /Applications/niq.app` on the recipient's machine | works today, no Apple account needed |
| Developer ID + notarisation (the `APPLE_*` secrets above) | no prompt at all, for everyone |

So an ad-hoc build ships `INSTALL.txt` — inside the DMG and next to it in
`dist/` — spelling out that one command. A notarised build ships neither, because
the recipient has nothing to do.

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
