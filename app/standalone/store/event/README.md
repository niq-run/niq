# store/event — the SQLite event store

`store/event` (package `eventstore`) is the durable implementation of
`event.AppendStore`: the core contract that extends `event.EventStore` with
`Append`. It is the one place that knows the SQLite schema, the indexes and the
query semantics behind `List`, `Get` and `Append`. The bus writes events
through it while routing; history and replay read them back.

## Schema and query semantics

`New` opens the database with its pragmas in the DSN — `busy_timeout(5000)`,
`journal_mode(WAL)`, `synchronous(NORMAL)` — so every pooled connection gets
them, then runs the idempotent migration: it creates the `events` table and the
indexes, drops the superseded `idx_events_time`, adds `request_id` to databases
that predate it, and only then indexes that column.

Recipients are stored twice: as a JSON column for display, and normalized into
`event_recipients`, one row per delivered-to worker. Worker filters go through
the normalized table so a match is exact equality on an indexed column instead
of a `LIKE` that would both scan and match substring ids.

`List` composes the filter in SQL: `WorkerIDs` (overriding the `workerID`
argument, where `"*"` or empty means all), `WorkerRoles` (`sent` /
`received` / both), trace id, request id, `Since`, `Types`, and the
`ExcludeTypes` / `ExcludeTypePrefixes` / `KeepTypes` blacklist, where a kept
exact type survives a covering prefix. It pages on `(timestamp, id)` through a
row-value comparison — a total order that survives asynchronous persistence,
unlike `rowid`. `Append` runs one transaction with a 5 s timeout and inserts
with `INSERT OR IGNORE`, so a replayed event id is not an error and its missing
recipient rows are still added.

## Files

| File | Responsibility |
|---|---|
| `store.go` | `New`/`Close`, the DSN pragmas, the in-place schema migration, `Append` and its recipient rows, `List` and `Get`, and the scan helper that decodes payload and recipients. |
| `store_test.go` | The exact/prefix type blacklist with keep overrides, the `request_id` round-trip and filter, the pre-`request_id` database upgrade, and the `EXPLAIN QUERY PLAN` check that the poll seeks its index. |

## Invariants

- **Pragmas ride the DSN**, never a one-off `db.Exec`: a single execution only
  reaches one pooled connection, leaving the rest at `busy_timeout=0`.
- **`Append` is idempotent per event id** and atomic across the batch; a
  failure rolls the whole batch back.
- **Pagination never uses `rowid`**, because insertion order does not match
  creation time; `(timestamp, id)` is the total order.
- **Recipient matching never parses the JSON column**, so `niq` cannot match
  `niq-workspace`.
- **`New` upgrades old databases in place** (column first, index second), so a
  store written by an earlier build opens without a migration step.
