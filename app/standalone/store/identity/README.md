# store/identity — `identities.json` as a `bus.Store`

`store/identity` owns one file per project, `<project>/identities.json`, and the
shape of the `bus.Identity` records inside it: worker id, type, remote flag,
publish and subscribe grants, credential. It implements `bus.Store`
(`Load` / `Save` / `Delete` / `All`) for the bus control plane, and adds
`Merge` so `store/migrate` can seed identities out of legacy inline ACLs
without knowing the file format.

The file is the durable source and there is no second copy. `New` reads it once
into memory; every mutation rewrites the whole file through a temp file and a
rename, and reads are map lookups behind a read lock. What the package does
not decide: who may register, when a grant changes, what a credential
authorizes. That is the bus control plane's job (`bus.Control`, implemented by
`core/impl/busctl`), which calls `Save` with the record it decided on.

## File shape

`{"schema_version": 1, "identities": [...]}`, identities sorted by
`worker_id`. Grants are written as they were given: a never-seeded identity
keeps `nil` grant lists, an explicit deny-all keeps empty ones. The
distinction matters, because a later connect uses it to decide whether the
type default should be seeded.

## Files

| File | Responsibility |
|---|---|
| `identity.go` | The `Store`, `New` with its read-once load, `Load`/`Save`/`Delete`/`All`, `Merge` for legacy seeding, and the atomic whole-file write. |
| `identity_test.go` | The nil-versus-empty grant round-trip, and `Merge` creating the file, keeping existing records and writing an empty merge. |

## Invariants

- **A missing file is an empty store**, not an error; the directory is created
  on construction.
- **Every write is the entire file**, sorted by worker id and replaced
  atomically; there is no append path and no partial update.
- **`Merge` always writes**, even with nothing to merge: the file's presence is
  what marks the legacy migration done.
- **`nil` and empty grants stay different** across a round trip, so "never
  seeded" and "deny all" are not conflated.
