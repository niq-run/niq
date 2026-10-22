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
| `ptc` | **the directory worker's tools** (capability discovery) + **the program worker's tools** (capability invocation) + this worker's own `message` | discover peers' capabilities with **directory** directly, then execute via a script run with **`program.run`** |

PTC exists to keep the tool list small and stable (better prompt-cache
behaviour, less tool-list churn) while still reaching the whole bus. The split
is deliberate: **discovery is the directory worker's job, invocation is the
program worker's** — so both are exposed directly. Every other peer's tools are
reached only from inside a script: the model writes one (`source` for a
one-off, or `write` + run by `name` to reuse) and the script calls tools through
`sdk.call(target, event, args)`. In PTC mode the worker seeds a **locked
instruction program** (`coding-ptc-tooling`) that renders into the system prompt
and explains this workflow.

### Focus filter (both modes)

A coding agent should not spend rounds on meta tools, so the tool-list policy
always drops:

- `context_compress` — compaction is automatic under budget pressure; the model
  never needs to call it;
- `program_query` / `program_update` — the coding agent's programs are fixed
  at creation time, so it neither reads nor rewrites its own program list;
- `provider_current` / `provider_list` / `provider_switch` — model management is
  a control-plane action. These stay **registered** (peers / the WebUI can still
  read and switch this worker's provider — the detail panel's provider switcher
  works), they are only hidden from the agent's own tool list.

In PTC mode it additionally drops the self **`request`** escape hatch, so peer
calls must go through `program.run` rather than an ad-hoc event call. The
agent-to-agent `message` channel stays.

The policy lives in the injected `ToolListBuilder`:

```go
func toolListBuilder(mode, programID, directoryID string) reasonBase.ToolListBuilder {
    if mode != ToolModePTC {
        return reasonBase.DefaultToolListBuilder
    }
    return func(w *reasonBase.BaseReasonWorker, caps []reasonBase.DiscoveredCapability) []llm.ToolDef {
        keep := caps filtered to (Source == w.ID() || Source == programID || Source == directoryID)
        return reasonBase.DefaultToolListBuilder(w, keep) // same naming + cache-stable sort
    }
}
```

Every other peer capability is dropped, so the equivalent tools are absent from
the LLM's own tool field — that is the point. `directory` is the exception that
stays: capability discovery is a first-class step, not something to reach
through a script.

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

Registered as worker type **`coding`**; params of interest:

| Param | Meaning |
|---|---|
| `tool_mode` | `normal` (default) or `ptc` |

Everything else (`provider` / `model` / `context_window` / `budget_*` /
`tail_tokens` / `compact_directive` / `goal` / `brief` / `readme` / `publish` /
`subscriptions` / `programs`) is shared with the generic reason worker.

## Files

| File | Responsibility |
|---|---|
| `worker.go` | `Config` + `NewWorker` + the `ToolListBuilder` policy + the PTC instruction program + the coding compaction addendum |
| `worker_test.go` | PTC vs normal tool-list assertions + PTC instruction seeding |
