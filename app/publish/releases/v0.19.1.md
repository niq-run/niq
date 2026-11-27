# niq v0.19.1

A patch for v0.19.0, which published without a macOS bundle.

## What went wrong

The release job passes every Apple secret to `release.sh` as an environment
variable, so with no signing identity configured, `APPLE_SIGNING_IDENTITY` arrives
as an **empty string**. Tauri takes a variable that exists at face value and ran
`codesign --sign ""`:

```
Signing with identity ""
: no identity found
failed to bundle project: failed codesign application
```

That happens before the ad-hoc signing block, which exists for exactly this case.
Every other artifact went out — the CLI archives, the Linux AppImage/deb/rpm and
the Windows msi/NSIS installers — so v0.19.0 is complete except for the macOS
`.dmg` and `.app.zip`.

## The fix

`release.sh` unsets an empty `APPLE_SIGNING_IDENTITY` before it calls
`tauri build`, so "no identity" and "an empty identity" mean the same thing. The
single-architecture path had the same hole; nothing local ever hit it, because a
local run does not export the variable at all.

CI now pins the variable empty in the desktop job, which is the state the release
job is in when no identity is configured — the previous setup did not pass the
Apple secrets at all, so the PR that introduced this was green while the tag was
not.

## Also in this release

- **MIT licence.** `LICENSE` now travels with the artifacts instead of living only
  in the repository: it is included in the CLI archives and the desktop bundle,
  copied into the npm package, and declared in the SDK's `package.json` and the
  app's `Cargo.toml`.
