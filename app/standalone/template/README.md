# template — project templates: presets, parsing, and zip import

`template` owns the seed of a project: a list of worker manifests plus an
optional `programs/` tree of resources. It parses and validates that file,
embeds the built-in presets in the binary, seeds them into the shared
templates directory on first run, and turns a shared zip package back into an
on-disk template. A validated `Template` is the input to
`project.CreateProject`, which instantiates each manifest.

It knows nothing about a project instance: no runtime state, no bus identity,
no lifecycle. Producing a template *from* a running project (export) is the
other direction and lives in `app/standalone/project/export.go`.

## Where it sits

```
preset/ (//go:embed) ──┐
                       ├──► Load / List ──► Template ──► project.CreateProject
common/templates/ ─────┘         ▲                         (one worker.Instantiate
                                 │                          per manifest)
ImportZip ───────────────────────┘
```

## Precedence

A template exists twice: as an embedded preset inside the binary and, after
seeding, as an editable directory `common/templates/<name>/` holding
`template.json` and optionally `programs/`. The disk copy always wins, for both
the JSON (`Load`, `List`, `ReadRaw`) and the resources (`CopyPrograms`); the
embedded preset is the fallback used before seeding. `Seed` runs once at gateway
start and writes only when the directory holds no template yet, so it never
clobbers what the user edited.

## Sharing

A shareable template is that same directory zipped: `ImportZip` requires
exactly one `template.json`, either at the archive root or inside a single
wrapping folder (which is stripped, and whose name can supply the template
name when none is given). Anything else is refused — a stray root file,
deeper nesting, files inside and outside the wrapper, an existing name — and a
failure during extraction removes what it wrote.

## Files

| File | Responsibility |
|---|---|
| `template.go` | The model (`Template`) and its validation, the embedded `preset` filesystem and its readers, `TemplatesDir` / `Seed`, disk-over-embedded resolution (`Load`, `List`), path helpers (`Dir`, `Path`, `ProgramsDir`) and copy helpers (`CopyPrograms`, `CopyDir`, `ReadRaw`). |
| `import.go` | `ImportZip`: the zip-package format, the single-`template.json` and root-resolution checks, the 64 MiB cap exposed as `MaxImportBytes`, and the extraction with its zip-slip guard. |
| `template_test.go` | Seeding is idempotent and produces `default`; a user-edited disk copy beats the embedded preset; the built-in preset carries no `provider`, `model` or `instruction`. |
| `import_test.go` | The import matrix: root and wrapped layouts, a name derived from the wrapper, missing name, invalid payloads, duplicates, and a traversal entry that must not escape. |
| `preset/default/template.json` | The built-in preset: seven manifests — `reason` (`niq`, carrying the `using-niq` playbook), `timer`, `host`, `directory`, `history`, `program` (mounted on the project's programs dir) and `workspace` (`ws`, mounted on workspace, programs and uploads). |
| `preset/default/programs/using-niq/PROGRAM.md` | The full playbook the default reason worker starts with: how to discover peers, address them, and split focus. |
| `preset/default/programs/using-niq/CHEATSHEET.md` | The one-page form of the same playbook, loaded on demand instead of the full guide. |

## Invariants

- **Declarations only.** A template is manifests plus resources; it carries no
  identity, credentials or history, so importing one never grants access.
- **`params` is the whole construction input.** The built-in preset deliberately
  omits `provider`, `model` and `instruction`; those come from
  `providers.json` and the instruction system.
- **The minimum a template must be.** At least one worker, each with a `type`
  and an `id`; a missing `schema_version` becomes 2.
- **Disk beats embedded.** Both the JSON and `programs/` follow the same
  precedence, so editing either one on disk is enough.
- **Imports are confined and bounded.** Names pass through
  `worker.SanitizeID`, entries may not escape the template directory, the
  uncompressed total is capped at 64 MiB, and nothing is written when the
  payload does not validate.
- **No project knowledge.** The package imports `store` only for `Root()` and
  `store/migrate` for `EnsureTemplates`; it never imports `launcher` or
  `gateway`.
