# coding — the coding reason worker

`coding` is a reason-family worker that runs **parallel to** the generic reason
worker: same shared mechanism (`core/impl/reason` — the reasoning round, budget,
tool dispatch, system-prompt rendering, lifecycle), its own composition and
specialisation for coding work.

It differs from the generic reason worker in exactly two places.

## 1. Tool-list policy: `normal` or `ptc`

`tool_mode` selects what the model's LLM tool list contains:

| Mode | Tool list | How tools are used |
|---|---|---|
| `normal` (default) | every discovered capability | like the generic reason worker |
| `ptc` | **only the program worker's tools** (+ this worker's own self tools) | the model discovers other workers' tools via the **directory** worker *from inside a script*, and executes them by running that script with **`program.run`** |

PTC exists to keep the tool list small and stable (better prompt-cache
behaviour, less tool-list churn) while still reaching the whole bus: the model
writes a TypeScript script (`source` for a one-off, or `write` + run by `name`
to reuse), and the script calls tools through `sdk.call(target, event, args)`.
In PTC mode the worker seeds a **locked instruction program**
(`coding-ptc-tooling`) that renders into the system prompt and explains this
workflow.

The policy lives in the injected `ToolListBuilder`:

```go
func toolListBuilder(mode, programID string) reasonBase.ToolListBuilder {
    if mode != ToolModePTC {
        return reasonBase.DefaultToolListBuilder
    }
    return func(w *reasonBase.BaseReasonWorker, caps []reasonBase.DiscoveredCapability) []llm.ToolDef {
        keep := caps filtered to (Source == w.ID() || Source == programID)
        return reasonBase.DefaultToolListBuilder(w, keep) // same naming + cache-stable sort
    }
}
```

Every other peer capability is dropped, so the equivalent tools are absent from
the LLM's own tool field — that is the point.

## 2. Compaction keeps the coding working set

The summarizer directive is the default one **plus** a coding addendum that
makes a compacted transcript preserve:

- the exact list of files already created/edited (absolute paths) and what
  changed in each;
- the current task state and next step;
- failed commands/tests and why.

So a compaction after a long coding session still knows *what has been touched*,
not just a prose summary.

> Follow-up (not implemented yet): track the edited-file set programmatically
> (from the worker's own write/edit tool calls) and re-inject it as a pinned
> note, instead of relying on the summarizer to keep it.

## Composition

`NewWorker(Config)` assembles a `reason.BaseReasonWorker` with the PTC/normal
`ToolListBuilder`, seeds the PTC instruction (PTC mode), sets the coding
default compaction directive, and reuses the default reason-family toolkit
(`reasonworkers.RegisterDefaultExtensions` — message / request /
context.compress / program.*).

Registered as worker type **`coding_reason_worker`**; params of interest:

| Param | Meaning |
|---|---|
| `tool_mode` | `normal` (default) or `ptc` |
| `program_worker` | id of the program worker to keep in PTC mode (default `program`) |

Everything else (`provider` / `model` / `context_window` / `budget_*` /
`tail_tokens` / `compact_directive` / `goal` / `brief` / `readme` / `publish` /
`subscriptions` / `programs`) is shared with the generic reason worker.

## Files

| File | Responsibility |
|---|---|
| `worker.go` | `Config` + `NewWorker` + the `ToolListBuilder` policy + the PTC instruction program + the coding compaction addendum |
| `worker_test.go` | PTC vs normal tool-list assertions + PTC instruction seeding |
