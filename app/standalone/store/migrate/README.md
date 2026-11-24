# store/migrate — the only reader of older file formats

`store/migrate` converts the pre-schema-2 layout into the current one, and is
the only package allowed to know what that layout looked like. It also renames
the retired schema-2 worker vocabulary in place, a name change rather than a
format change. The runtime reads schema 2 exclusively, so compatibility
branches never reach the worker model or project storage. `store` calls
`EnsureProject` before a project or manifest read; `template` calls
`EnsureTemplates` before listing or loading a template.

## What a legacy project becomes

| Before | After |
|---|---|
| `project.json` with `workers`, `archived` and typed worker fields | `project.json` with metadata only |
| worker entries inside the metadata | `workers.json` and `workers.archived.json`, one `WorkerSet` each |
| inline `publish` / `subscriptions` / `credential`, and `id/identities.json` | `<project>/identities.json`, written through `identity.Merge` |
| `id/root_secret` | `root_secret` at the project root, bytes unchanged so issued tokens still verify |
| — | `migration-report.json` with the active and archived counts and the orphan ids |

A worker's typed fields, `env` and unknown top-level keys all fold into
`params` (identity and metadata keys are dropped with a log instead), and a
conflicting duplicate is an error rather than a silent override. `managed` /
`remote` resolve to a placement when none is declared, `unmanaged-process`
keeps no `params`, and only `unmanaged-process` keeps a credential. Identities
that belong to no worker are dropped and reported, not carried along.

## Convert once, on first read

The gate is `schema_version >= 2` plus a `workers.json` on disk; schema-2
metadata with no declaration file gets an empty `workers.json`, not a second
conversion. A converted project costs a read of `project.json` and of each
declaration file, plus one stat of `identities.json`, per call; it writes
nothing unless there is a name to normalize, and a second run leaves the bytes
untouched. Backups are taken before the first rewrite (`project.json.bak`,
`identities.json.bak`). The only files removed are the old
`id/identities.json` (and an `id/` left empty) and a redundant legacy
`root_secret` when the root copy already exists.

## Files

| File | Responsibility |
|---|---|
| `migrate.go` | `EnsureProject`, `EnsureTemplates`, `convertWorker`, placement and identity conversion, `migrateInlineIdentities`, and the retired type/id renaming (including the app-hosted WebUI declaration that is dropped rather than renamed). |
| `migrate_test.go` | The full legacy-project conversion, template conversion, and the idempotence of name normalization. |

## Invariants

- **Runtime code never sees a legacy shape.** Conversion is a precondition of a
  read, not a repair pass someone remembers to run.
- **Every conversion is idempotent**, and re-running it changes no bytes.
- **User prose is never rewritten.** Retired names are fixed in declaration and
  ACL fields; free text is left for a human.
- **A broken template worker is skipped, a broken project worker fails.** The
  template loop logs and continues; the project loop returns the error.
