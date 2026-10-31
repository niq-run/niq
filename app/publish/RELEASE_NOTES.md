# niq v0.13.0

An execution release: script programs no longer spawn a process. `program.run`
now runs inside an in-process QuickJS VM — a run starts in 0.7 ms instead of
37 ms and needs 0.2 MB instead of 43 MB, and the sandbox contract becomes a
property of the environment rather than a rule the script is asked to keep.

Riding along: a batch of correctness and performance work that had accumulated
since v0.12.0 — a reply that could answer a reply and write a million rows,
history pages that scanned a 1.2M-row store, a directory that could not answer
`worker.help`, and providers that can now be named and that declare what they
can actually do.

## Highlights

- **Script programs run on QuickJS, in-process.** No `node` to install, no
  launcher on disk, no stdio protocol. `import`, `require`, `process` and
  `fetch` do not exist in the sandbox; memory and CPU have hard ceilings.
- **History pages seek instead of scanning.** Page 1 on a 1.2M-row store:
  2.2 s → 0.6 ms. A worker-scoped page: 0.7 s → 14 ms.
- **A reply can no longer answer a reply.** The no-handler fallback used to
  bounce failure replies between two workers forever; it once ran at ~1900
  events/second for ten minutes and wrote 1.17M rows.
- **Providers are named, with per-model limits.** Credentials live in
  `provider.json` only, and each model declares its max output and input
  modalities — which is now actually honored by the reason worker.
- **Worker display names and human approvals.** Workers can carry a label
  (`备注名`), and `approval.request` is a first-class capability with a pending
  count in the talk header.
- **The talk view folds process events.** A turn reads as input →
  `[12 process events]` → response.

## 1. `program.run` runs on QuickJS

The executor is now `quickjsexec`: every run gets a fresh QuickJS VM
(`modernc.org/quickjs`, the engine compiled to Go — no cgo) instead of a
spawned `node` process speaking JSON Lines over stdio.

What changes for a script:

- **The globals are ours.** `import`, `require`, `process`, `fetch` and the
  Bun/Deno globals are absent, not merely forbidden. Reaching for them fails at
  runtime rather than doing something surprising.
- **Resources have hard ceilings.** `SetMemoryLimit` bounds allocation (64 MiB
  per run by default) and a watchdog interrupts a script that never yields.
- **A run is cheap.** ~0.7 ms and ~46 KB allocated for a one-call script,
  against 37 ms and a 43 MB process. No launcher to materialize, no protocol
  stream to parse, no process teardown.
- **Errors keep their location.** QuickJS reports structured line/column for
  parse errors, and `<eval>` frames are mapped back to the script body, so an
  error still names the failing line of your own source.

The SDK contract is unchanged: `sdk.call` / `message` / `log` / `result` /
`args`, structured replies arriving as real objects, `Promise.all` fanning out
concurrently, `console` working (mapped onto `sdk.log`). The reply shape is the
same `{output, log?}` object.

The trade-off is deliberate and documented: the engine now lives in niq's
address space, so a bug in it is a bug in niq. That is why the per-run memory
limit and the single-owner discipline are load-bearing rather than optional. A
real isolation boundary (wasm, container, remote executor) remains a drop-in
replacement behind the same `program.Executor` interface.

Measured on a real project's scripts: pure overhead 0.6–1.0 ms per run, 16
concurrent runs peak at 36.7 MB, and 1000 sequential runs hold at ~15 MB.

## 2. Event store and history paging

A 1.2M-row store answered a single history page in 0.7–2.2 s. Three causes, all
in how the query met the indexes:

- The pagination anchor was an OR of two timestamp tests around a scalar
  subquery, which SQLite cannot turn into an index range bound. It is now a row
  comparison: `(timestamp, id) < (SELECT timestamp, id FROM events WHERE id = ?)`.
- The worker filter was an OR across three sources, so no single index could
  serve it and every row ran a correlated subquery. Matching rowids are now
  collected per indexed probe and filtered with `rowid IN (...)`.
- The legacy single-worker path matched recipients with `LIKE '%id%'` — a full
  scan that also false-matched substring ids (`niq` vs `niq-workspace`).

`ORDER BY` carries `id` as the tiebreak, so the index is now
`(timestamp DESC, id DESC)`. Existing stores get it on their next open
(~2 s for a 553 MB store).

An index on `request_id` also turns the by-request lookup from a 1.2M-row scan
into a seek, and the reply pollers ask once before waiting instead of sleeping
first.

## 3. Replies never answer replies

A `request.failed` reaching a worker with no handler for it was answered with
another `request.failed`; the peer did the same, and two workers fed each other
failure replies indefinitely. This ran in production: `program` and `workspace`
bounced "does not serve event request.failed" at ~1900 events/second for ten
minutes, writing 1.17M rows (166 MB).

Reply events (`request.completed` / `failed` / `rejected` / `progressed`) are
now dropped when unhandled, both in the no-handler fallback and in the
internal-capability refusal. `request.cancel` deliberately stays a request — it
is how a caller abandons a pending call, so an unknown one is still answered.
The program worker additionally consumes orphan replies (a reply matching no
pending script call) and logs the drop, so a late reply stays traceable.

## 4. Providers: by name, with per-model limits

- **Providers are named.** A reason worker names a provider from
  `provider.json` plus a model. The bare `api_key` / `base_url` spawn params and
  the ad-hoc provider fallback are gone, so credentials live in exactly one
  place and an unknown name fails the spawn.
- **Models declare `max_output` and `input_types`** (`text` / `image` /
  `audio` / `video`), with provider-level defaults merged into the
  `model_details` that `provider.list` reports. The editor exposes them per
  model: a max output field and one checkbox per modality, none ticked meaning
  "unknown, assume supported".
- **The output cap is consumed.** The reason worker resolves it next to the
  context window and hands it to the provider as the round's max tokens.
  Previously every round fell back to the adapter's default (Anthropic
  hardcodes 4096), silently truncating models that can write far more.
- **The context window is re-derived on every switch** — the old single field
  kept the startup model's 1024k across a switch to a 262k model.

Input modalities stay declarative for now: the message model carries text and
image blocks, so audio/video are eligibility metadata until content blocks and
adapters exist.

## 5. The directory answers `worker.help`

The system prompt tells the model to send `worker.help` when a contract is not
enough, but the directory — where every discovery starts — had no handler and
answered "does not serve event worker.help". `search` and `describe` now serve
static guides: search's two modes (roster vs keyword), how `types` and
`exclude_types` compose (exclusion wins), what `describe` is the authority for,
and how it differs from `worker.readme`.

## 6. Worker names, approvals, and the talk view

- **Display names.** `WorkerConfig` gains a `label` (`备注名`), settable at
  spawn or in the declaration, persisted in `project.json` and shown in the
  sidebar, the detail header, the worker list and talk badges. Rename inline
  from the worker row's context menu; an empty value falls back to the id.
- **HIW approvals.** `approval.request` is a registered capability, so it rides
  `worker.ready`'s watch with an open schema instead of a private switch
  branch. The talk header carries a pill with the pending count, and the
  send-event form shows the request payload immediately and fills in the reply
  whenever it lands.
- **Process events fold.** A run of thinking / tool / interruption rows
  collapses into one chip; the reader's scroll position is restored after a
  toggle, because a big tool list changes the timeline height by thousands of
  pixels. Decided approval requests start folded, and approvals the human raised
  themselves are no longer filtered out of the conversation.
- **Smaller WebUI fixes.** Native checkboxes are drawn from the palette instead
  of the UA's white square; the theme attribute is mirrored onto `<html>` so
  portaled menus match; the quote box ellipsizes correctly; the old "Compact
  Mode" density toggle is gone in favor of streaming / expand thinking /
  collapse events / response only.

## 7. Smaller changes

- **`run_program` is now `run`.** The built-in tool's wire name matches the
  program worker's `run` event, because that is the event: the tool is that
  call pre-addressed to the program worker. The two are told apart by owner
  (`run` is this worker's tool, `program__run` is the peer's).
- **`install.sh` moved to `app/publish/install.sh`**, next to the rest of the
  distribution. The raw.githubusercontent URL changes, so external copies of the
  install command need the new path.
- **The system prompt was rewritten** around event-only collaboration,
  directory-first discovery, and the three built-in sends (`request`, `message`,
  `run`).
- **`script_executor_args` is gone** with the subprocess, and so is the
  installer's `node` check.

## Upgrade notes

- **Go 1.26 is required** to build from source: `modernc.org/quickjs` v0.27
  declares it, and `go.mod` moved accordingly. The release workflow reads the
  version from `go.mod`.
- **`node` is no longer needed** at runtime. A machine that only managed
  programs before can now execute them.
- **Providers must be named** in `provider.json`; the `api_key` / `base_url`
  spawn params no longer exist.
- **`script_executor_args` is ignored.** It configured the subprocess command
  line, which no longer exists; remove it from presets at your leisure.
- **Scripts lose `setTimeout` and `Intl`.** Neither exists in QuickJS and
  neither is shimmed; `console` is. Scripts that reached for node globals
  (`process`, `fetch`, dynamic `import`) now fail at runtime instead of working
  by accident.
