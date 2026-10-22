# niq v0.10.0

This release carries **no functional changes** since v0.9.0. It ships the
release tooling move described below and rebuilds the same binaries; behaviour
is identical to 0.9.0.

## What changed

- **Release notes live with the release machinery.** Notes are now archived
  one file per release under `app/publish/release-notes/vX.Y.Z.md`, with
  `app/publish/RELEASE_NOTES.md` holding the current release's notes.
- **The release workflow reads them.** For a `v*` tag, it uses the tag's own
  archived file, falls back to `app/publish/RELEASE_NOTES.md` only when that
  file names the tag, and otherwise lets goreleaser generate a changelog. This
  replaces the old root-level `RELEASE_NOTES.md` check, so GitHub releases can
  carry written notes instead of a commit list without editing a shared file
  every time.

## Upgrade notes

No action needed. The CLI, workers, SDK and embedded WebUI are unchanged from
v0.9.0.

## Feature overview (from v0.9.0)

PTC script programs (`program.run` with `path` / `source`, a node subprocess
executor, structured `{ output, log? }` replies), the `coding` worker with
`normal` / `ptc` tool modes, the unified reply contract (an object reply is the
payload; every built-in worker declares its `Response` in `worker.ready`),
TypeScript SDK 0.5 / 0.6 (schema builders, `response`, `worker.discover`), and
the WebUI polish round (four-group type tag colours, response copy, pinned
worker ordering, SSE reconnect).

See the v0.9.0 release notes for details.

## Validation

- `go build ./...`, `go vet ./core/... ./app/...` clean.
- `go test ./core/... ./app/...` — all packages pass.
- Release binaries for darwin / linux / windows on amd64 + arm64 built by the
  release workflow, with `niq --version` reporting `0.10.0`.
