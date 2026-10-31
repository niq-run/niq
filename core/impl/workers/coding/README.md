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
| `normal` (default) | the reason worker's tool list, unchanged: `message`, `request`, `run` | discover with `request(directory, "search")`, then call with `request(target, event, args)` |
| `ptc` | `message` + `run` | discover **and** act from inside a script: `sdk.call("directory", "search", …)`, then `sdk.call(target, event, args)` |

Normal mode does not widen the list because the worker writes code: a coding
agent calls the bus exactly like any reason worker. The one coding-specific
policy is PTC, which drops `request` so that **every** event send — discovery
included — happens inside a script that `run` executes (`source` for a
one-off, or `write` + run by path to reuse). No capability is lost, only the
route: peer tools that normal mode would call directly become
`sdk.call(target, event, args)` in the script. In PTC mode the worker seeds a
**locked instruction program** (`coding-ptc-tooling`) that renders into the
system prompt and explains this workflow.

### The policy is a scope, not an exclusion list

Both modes are a `reasonBase.ToolScope`: which of the worker's own events become
tools (`Own`), which peer worker types contribute their whole surface
(`PeerTypes`), and which contribute only named events (`PeerEvents`). Nothing
peer-supplied is exposed by default any more — discovery is a `request` away:

| Mode | Scope |
|---|---|
| `normal` | `DefaultToolScope` — `Own: message, request, run` |
| `ptc` | `Own: message, run` |

Two properties fall out of the model instead of out of a list to maintain:

- **Meta events are never tools.** readme / readme.update / help describe a
  worker rather than do work, so `ScopedToolListBuilder` drops them for every
  source (`reasonBase.IsMetaCapability`). The coding agent's
  `context.compress`, `program.*` and `provider.*` stay **registered** — peers,
  the WebUI and the control plane still call them (the detail panel's provider
  switcher works) — they simply are not in `Own`.
- **PTC drops `request` by omission.** `message` and `run` are in `Own`,
  `request` is not, so every capability call goes through a script rather than an
  ad-hoc event send.

```go
func toolListBuilder(mode string) reasonBase.ToolListBuilder {
    if mode != ToolModePTC {
        return reasonBase.ScopedToolListBuilder(reasonBase.DefaultToolScope)
    }
    return reasonBase.ScopedToolListBuilder(ptcScope)
}
```

No peer capability is exposed in either mode, so the equivalent tools are absent
from the LLM's own tool field — that is the point. Discovery is not an exception:
`request(directory, "search")` in normal mode, `sdk.call("directory", "search", …)`
in PTC, through the same built-in send either way.

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
