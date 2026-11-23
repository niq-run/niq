# store — the on-disk shapes of the single-machine form

`store` is the only package that knows what the standalone form keeps on disk.
`root.go` resolves the configuration root and the paths this package owns
beneath it; `project.go` defines the stored shape of `project.json`;
`manifest.go` owns `workers.json` and `workers.archived.json` plus the
`worker.ManifestStore` the control plane drives; `provider.go` is the file half
of provider config; `state.go` persists in-process worker runtime state.

The dependency is deliberately inverted. `store` imports the domain *value
types* it serializes (`worker.Manifest`, `provider.Config`) and the domains
import nothing from `store`; `provider.Loader`, the one function type that
crosses back, is declared in `provider` and bound to `store.LoadProvider` by
the assembly. `store` imports no `project`, `launcher` or `app/client`, so it
cannot depend on how a project runs. `root.go` is the only file that turns a
project id into a directory; every other file takes that directory.

## Two project shapes

`store.Project` is the file; `contract.View` is the response. `View` adds the
resolved `Dir` and the probed `Running` flag and omits the internal
`schema_version` and `upload_dir`; `launcher.ViewOf` is the only conversion.
A field added to `project.json` therefore changes nothing on the wire until it
is also added to `View`.

`SaveProject` stamps `SchemaVersion` when it is zero, refuses an empty id, and
writes through a temp file and rename. `LoadProject` runs the one-time
conversion first, so a legacy `project.json` is never parsed as the current
schema.

## Layout

Every path in this table derives from one root: `NIQ_HOME` when set, otherwise
`<home>/.niq`.

| Path | Holds |
|---|---|
| `<root>/projects/<id>/` | one project; `<id>` passes through `SanitizeID` |
| `…/project.json` | `Project` |
| `…/workers.json`, `…/workers.archived.json` | `WorkerSet`, the same shape |
| `…/programs` | the project's default program mount |
| `…/root_secret` | the project's token-signing secret |
| `…/logs/niq-YYYY-MM-DD.log` | the project log; the date is the rotation |
| `<root>/common/auth.json` | basic-auth file (`NIQ_AUTH_CONFIG` overrides it) |
| `<root>/common/providers/provider.json` | provider config (`NIQ_PROVIDER_CONFIG` overrides it) |

Two files that live beside a project are named by their callers, not by
`root.go`: `identities.json` is joined by the assembly and owned by the
`identity` sub-package, `events/events.db` likewise by `event`, and `State`
receives whatever root the assembly chose. Their shapes live in those
sub-packages, not here.

## Manifest files

Both files are a `WorkerSet`: a schema version plus a list of
`worker.Manifest`. A missing file, a nil list and a zero schema version all
read as "empty, current" rather than an error, because a project may
legitimately have no workers. Archiving is a move between the two files, never
a delete: the manifest is preserved verbatim so restoring it resumes the
worker unchanged.

## Files

| File | Responsibility |
|---|---|
| `root.go` | The root (`NIQ_HOME`, `~` expansion), every path in the layout table, `SanitizeID`, and the two address helpers: `LocalhostURL` and `PortOf` (delegating to `helper.Port`). |
| `project.go` | `Project`, `ProjectFile`, `LoadProject`, `SaveProject`: the typed reader/writer for one `project.json`, including the conversion-before-read rule. |
| `manifest.go` | `SchemaVersion`, `WorkerSet`, the two manifest paths, load/save, `FindWorker`, `ArchivedIDs`, the write lock, archive and remove moves, the `worker.ManifestStore` implementation, and the atomic JSON writers the other files share. |
| `provider.go` | `LoadProvider`, `SaveProvider`, `EnsureProviderExample`: `provider.json` IO, a missing file as an empty config, and a skeleton seeded once and never overwritten. |
| `state.go` | `State` implementing `workerctl.WorkerStore`: `SaveState`, `LoadAll`, `Delete` over `<stateRoot>/<workerID>/state.json`, with the snapshot kept as raw JSON. |
| `provider_test.go` | The provider round-trip and seed-once rule (both driven through the `NIQ_PROVIDER_CONFIG` override), plus the `NIQ_HOME` layout the package's path helpers depend on. |

## Invariants

- **One write lock for manifest files.** A process-wide mutex serializes
  read-modify-write cycles in every project; reads take no lock, because the
  rename hands them a complete file.
- **Archive writes destination first.** A crash between the two writes leaves
  the manifest in both files (repaired by the next archive) rather than in
  neither; `Remove` deletes from both.
- **Schema 2 only.** `SchemaVersion` is the format this package reads and
  writes; an older file is `store/migrate`'s job and is converted before it
  gets here.
- **`provider.json` is optional.** A missing file is an empty config, not an
  error, and `EnsureProviderExample` never replaces an existing one.
- **`state.json` is the one file written in place.** The metadata files go
  through temp+rename; runtime state is a single `WriteFile`, and `LoadAll`
  skips a worker directory it cannot parse instead of failing the whole load.
