# reason — the domain-agnostic reasoning base

`reason` is niq's domain-agnostic reasoning mechanism, shared by every
reason-family worker (e.g. the generic reason worker).
It consumes events from the bus, calls the LLM, and publishes outcomes as
events. An embedding worker composes its own subscriptions, tools, event
conversion and goals (programs) onto `BaseReasonWorker`.

## Architecture

```
watch() — the event-fetch goroutine
  │
  ├─ sleep point: <-busCh (block wait, zero CPU)
  ├─ process(evt) — dispatch a single event (see process.go)
  │     each handler translates the event into a transcript input + scheduling
  └─ tryReason() — the decision gate
        └─ needReason && !isReasoning → go reason() (own goroutine)
              │
              ├─ LLM call (seconds, does not block the event loop)
              ├─ tool calls → insert placeholders → dispatch to target → return
              └─ text response → publish → return
                    └─ tryReason() — pick up overlapping events
```

Division between the loop and dispatch:

- **worker.go** — the event loop goroutine (`watch`) + the `tryReason` decision gate, next to the `Start`/`Stop` lifecycle that runs them.
- **process.go** — `process` dispatch + its handlers + event→transcript-input
  translation.

## Files

| File | Responsibility |
|---|---|
| `worker.go` | `BaseReasonWorker` — the generic reasoning body (embeds `baseworker.BaseWorker`) + `Config` + `NewBaseReasonWorker` + lifecycle (`Start`/`Stop`/`Snapshot`/`Restore`/`Messages`) + the event loop (`watch`) + the `tryReason` decision gate + the exported accessors the concrete worker builds on (`Transcript`, `TryReason`, `LLMToolDefs`, ...) |
| `reason.go` | the reasoning round: prepareReasoning / consumeStream / finishReasoning + the worker's own event-type constants (lifecycle `reason.*` broadcasts + the generated self tools `message` / `request`) + tool dispatch handleToolCalls + `handleContextBudget` (context-window pressure) + tool-call id synthesis |
| `process.go` | event dispatch `process` + handlers (abort/timeout/tool-result/input) + the event→transcript-input conversion (`DefaultConverter` / `ConvertInputEvent` / `resultOutcome`) |
| `capability.go` | **capability**, both directions plus its applications. Inbound `worker.ready` / `worker.gone` handling + the `DiscoveredCapability` universe; outbound `BroadcastReady` (two-batch: peers then self) / `BroadcastDiscover` (via `WatchEntries`); resolution over the universe — `toolNameOfCapability` / `capabilityByToolName` / `findProvider` / `isRequestCap`; the convention capabilities the mechanism itself invokes — the `invokeCapability` primitive with `invokeContextCompress` (self) / `invokeElapseReminder` (peer) and their names; and applying the universe to the LLM — `ToolListBuilder` / `LLMToolDefs` (what the model sees) |
| `provider.go` | provider management, exposed through the extension mechanism: `ProviderSources` / `ProviderInfo` + `registerBaseExtensions` (provider.switch/list/current) + `setActiveProvider` / `resolveContextWindow` |
| `readme.go` | the worker's self-description: the generic `worker.readme` read capability + the advertised `worker.readme.update` write, seeded from `params.readme`, with runtime updates persisted in the snapshot |
| `llmerror.go` | LLM error policy: context overflow → compression request, rate limit → backoff reminder, 4xx → fail, else retry |
| `systemprompt.go` | renders the system prompt from programs |

Self-contained sub-domains extracted as subpackages:

| Package | Responsibility |
|---|---|
| `core/impl/reason/transcript` | the `Transcript` interface + sealed `TranscriptPatch` variants (the context-construction algebra) + `AccumulateTranscript`, the default accumulate implementation — self-synchronized, with meta-edit buffering |
| `core/impl/reason/requesttracker` | `RequestTracker` — lifecycle of the requests this worker issued under each capability's own event type (Pending/Parked, late results) + `PreemptCause` (why a wait/round was preempted) |

Note on vocabulary: a tool call has two names. On the **LLM side** the
transcript holds tool calls (`ToolPlaceholdersPatch`, `ToolResultPatch`, ...).
On the **bus side** this worker sends an invocation event under the
capability's own event type, tracked by `RequestTracker`. Capability handling
lives in one place: `capability.go` holds both directions — learning others'
contracts and declaring this worker's own — the resolution over them, and the
two ways the universe is applied (the LLM tool list and bus invocations).

The shared base every built-in worker embeds — `BaseWorker` (identity,
subscriptions, tool-request replies, arg helpers) plus the generic `Extension`
registry (`Register` / `DispatchExtension` / `Extensions`) — lives in
**`core/impl/baseworker`**, not here. `core/itfs/worker` remains contracts only
(`Worker` / `ManagedWorker` + the `Tool` / `ToolFunc` data types).

Vocabulary split by perspective: **`Extension`** (core/impl/baseworker) is the
declaration side — how a worker extends itself, "what it responds to and how".
**`DiscoveredCapability`** (this package, capability.go) is the observation
side — what other workers offer on the bus, as learned from worker.ready.
The same fact, two names: extension is the mechanism, capability is the
interface. The wire format bridges them and is unchanged.

The generic tools (`send_message`, `request`) and the context meta op
(`context.compress`) are **not** here — they are the default reason worker's own
toolkit (`core/impl/workers/reason`). The mechanism only declares
the `context.compress` convention and fires it under window pressure; shrinking
the transcript is the worker's strategy.

## Core ideas

### The transcript is the workbench notes

The only cross-round state of a reason node is the transcript — workers (single
reasoning rounds) take turns at the workbench, each reading the notes the
previous one left, reasoning, and writing back. The transcript is not the
worker's core state; it is a projection of facts plus working memory, held in a
`Transcript` (transcript.go). Render is an identity projection of the
transcript; the `[system]` prompt comes from the worker's programs
(`systemprompt.go`), not the transcript.

The transcript is self-synchronized: it carries its own lock and an editing
state (`BeginEdit`/`CommitEdit`/`AbortEdit`). An edit that must rewrite it
over a long, off-transcript computation (an LLM summary) begins by taking a
snapshot, computes without holding any lock, then commits; concurrent external
inputs are buffered and merged on commit, so the event loop never blocks and
inputs are never lost or torn.

### Event dispatch

All events go through `process()`. The three input levels (`input_mode`) are a
spectrum of increasing intrusion, not a "default vs non-default":

| level | behavior | sets needReason? |
|---|---|---|
| schedule (level 1) | leave a message; only trigger a round when idle | when idle |
| append (level 2) | record cause + schedule next round, don't interrupt in-flight | yes |
| interrupt (level 3) | interrupt in-flight + schedule + park (hiw usually takes this) | yes |

Other events are dispatched by `process`:

| event | effect | sets needReason? |
|---|---|---|
| worker.discover | reply worker.ready | no |
| worker.abort | park + recall + record | no |
| timer.timeout | park leftover tools (cause=timeout) + schedule | yes |
| worker.ready/gone | learn/forget the worker's tools | no |
| request.completed/failed/rejected | resolve or park-late + update transcript | when resolved |
| capability event (send_message, context.compress, provider.*, ...) | dispatch to the registered extension by event type (own Internal events arrive addressed to self); an async meta op schedules a round when done | no (meta op: when done) |
| worker.input | handle per the three input_mode levels (a reminder timer's fire is delivered here too) | depends on level |

### Tools, extensions and the request-response convention

A reason worker serves tools through the extension registry (core/impl/baseworker):
each registered extension declares the event it responds to and the handler
that serves it. The LLM-facing tool list is the builder's view over the
discovered capability universe (core/impl/reason/capability.go) — own and peer tools
alike, named `provider__name` for peers. Each capability is invoked under its
own event type (the old `tool.request` dispatch event is retired); invoking
any capability is answered with the one request-response convention:
`request.completed` (result) / `request.failed` / `request.rejected`,
echoing the request's `RequestId` (empty id = notification, no reply).

A meta extension (context.compress, provider.switch, ...) is a direct edit of
this worker's own state — it is registered with its **own event type**, not
multiplexed into a single shared update event. When the LLM calls one, `handleToolCalls`
reroutes it into that event type sent to self (e.g. `context.compress`), on
the same auditable bus path as every other invocation. The completion echoes
the request's id via the same `request.*` convention. The system-side
compaction triggers (hard budget, `ErrorContextLength`) funnel through the
same `context.compress` event.

### Self-description (readme)

Every reason worker carries a readme in two parts: **what it is responsible for**
(its scope and what it is for) and **what it has recently been working on** (its
current focus, the last piece of work it carried, what a peer handing it more
work should know). It is seeded from `params.readme` at creation and then owned
by the worker — the point is that after weeks of drifting, the description still
says what the worker actually became and where it currently is, not what the
spawner guessed.

The first part is **identity, not policy**. It answers "what is this worker";
rules, policies, standing instructions and step-by-step procedures answer "what
must it do and how", and belong to **programs** (instruction / playbook content
on the program worker). A worker that writes its rulebook into the readme has put
it where the reasoning loop never reads it as a rule: the tool description and
the system prompt both say so to keep that drift out. The second part is a short
status line, refreshed when the focus moves on — it is not a work log, and it
must not turn into one.

- `worker.readme` (peer-facing) returns the current text, so the recall flow is:
  list/search the roster (directory), then read the readme of the candidates to
  confirm which worker is the right one — and what it is busy with right now.
- `worker.readme.update { readme }` replaces it. It is advertised like the
  read half: the WebUI and authorized peers can discover it in the roster, and
  the bus ACL — not the announcement — decides who may send the update.
- The value is durable. An update signals the host to checkpoint; `Snapshot`
  always carries the current text (a pointer, so a deliberate clear survives),
  and `Restore` overrides the config seed. A managed restart is a fresh rebuild
  and therefore re-seeds from `params.readme`.

### Tool-call lifecycle (placeholders)

The LLM API forbids inserting a non-tool_result message between
`assistant(tool_calls)` and its `tool_result`. When reason dispatches tool
calls it immediately inserts `tool_result(pending)` placeholders; a result
replaces it in place, parking replaces it in place with an explanation, and a
late result is appended as a user message (a second tool_result for the same
call_id would violate the pairing invariant). This pairing invariant lives in
the transcript (transcript.go); the worker translates its lifecycle into sealed
`TranscriptPatch` variants handed to the transcript.

`RequestTracker` (core/impl/reason/requesttracker) records only requests still being awaited:
- **Pending** — active, the reasoner is still waiting.
- **Parked** — no longer awaited, kept in case a late result arrives
  (`ParkCause` distinguishes input/timeout/abort/restart).

Persistence: the tracker rides along in every snapshot (`Snapshot` captures the
transcript and the tracker together — a `[pending]` placeholder is never
persisted without its request). On `Restore` a rebuilt wait is always parked
(`PreemptCauseRestart`): the invocation was issued by an instance that no
longer exists, and in practice its target does not have it either (a restarted
peer has forgotten it; nothing reaps a pending call on worker.gone). A restored
Pending could therefore never resolve — and since `Resolved()` gates
schedule-mode input, that wait would now survive restarts. Parking keeps
tracker and transcript in step (the placeholder is rewritten with the
explanation) and a result arriving afterwards is matched as a late result
instead of being dropped. Restored calls are never re-dispatched (side-effect
risk), and the synthesized tool-call id counter (`toolCallSeq`) is persisted
too: its uniqueness domain is the whole transcript, so re-minting from 0 after
a restart would collide with ids already in history and a result would patch
the wrong message.

### Context budget and compaction

After each streamed round the worker records usage from `Usage` (latest
Input+Output snapshot, never accumulated) and compares it against
`ContextWindow`:

All three triggers funnel into one auditable path: each emits the
`context.compress` convention event to this worker (a meta operation on its own
state, registered under its own event type — never multiplexed into a single
shared update event), so compaction is always reachable through the same bus
event — never a bespoke side channel.

- ≥ soft line (0.85) → the system injects a reminder; the LLM decides and calls
  `context.compress`.
- ≥ hard line (0.97) → the system emits `context.compress` itself, without
  waiting for the LLM.
- opening the stream hits `ErrorContextLength` → the round ends (a non-retriable
  error: retrying cannot shrink an over-length context); `context.compress` is
  emitted, compaction completes asynchronously, and the next round starts on the
  shrunk context.

Compaction = snapshot via `BeginEdit` → summarize the projected transcript →
`CommitEdit(digest, tailTokens)`. The summarizer prompt is overridable via
`compact_directive` (program/config); a built-in fallback exists, and when a
digest is already present the merge is incremental (early goals/constraints
survive). Projection comes first: strips image/thinking, truncates oversized
tool results. The retained tail is bounded by a **token budget** (`tailTokens`,
not a fixed message count) and the cut snaps to pairing boundaries, so a tool
call and its results are never split. Because the transcript is
self-synchronized, compaction runs on its own goroutine: the summary is
computed without holding any lock, and inputs arriving meanwhile are buffered
and merged on commit, so the event loop never blocks.

### Tool-call timeout

reason recognizes the timeout tool by a bare-name contract: if any peer provides
a capability whose bare name is `timeout`, reason treats it as this round's
tool-call timeout (provider-agnostic — the timer worker need not be named
"timer"). If no such capability exists the feature is disabled. The provider is
resolved from the discovered universe, recorded as `activeTimeoutProvider` at
set time, and cancelled on completion (or acted on when the timeout fires).

### Abort recall

On abort, reason sends directed `request.cancel` events to each target worker —
a best-effort recall. Parked calls stay in the tracker; a late result is
surfaced to the LLM as a late-result message carrying its cause.

## Design principles

1. **process does not call the LLM.** Event handling is microsecond-level: it
   only routes and updates state; reasoning is started from tryReason.
2. **Event side and reasoning side are decoupled.** Events only change state
   and set needReason; the reasoning side consumes needReason and never
   reverse-infers a cause.
3. **reason is async.** reason() runs on its own goroutine so the watch loop
   stays responsive; `cancelReason()` can genuinely interrupt in-flight
   reasoning.
4. **reason does not recurse.** One result per reasoning; chaining happens
   through tryReason re-entry (isReasoning guarantees at most one round).
5. **Concurrency is lock + snapshot; the transcript is self-synchronized.**
   Shared worker fields are read/written under `w.mu`; the transcript carries
   its own lock and a meta-edit state, so a long summary holds no worker lock
   and never blocks the event loop.
6. **No internal queue.** process consumes at microsecond level; the bus
   channel drains in real time.
7. **Focus on one worker's attention.** Transcript = workbench notes; tools =
   own call-and-answer built-ins plus discovered external workers; programs are
   the educable source of behavior.