# program — the Program worker

`program` is niq's domain-service worker for **Programs** — the reusable
"source" a worker loads and that compiles into tool calls. It manages a
Program's lifecycle (search / load / write / edit / upsert / delete on the bun)
and, for **script programs**, executes them.

Two axes define a Program (`core/itfs/program`):

- **ContentType** — what it says: `instruction` (a binding rule) or `playbook`
  (a procedural how-to).
- **FormType** — how it is written: `prompt` (natural language, "compiled" by
  the LLM at inference time) or `script` (a formal script interpreted into tool
  calls). A script program is the script-form counterpart of a playbook: both
  describe how to run a sequence of tool calls, one in prose and one in code.

## Two backends

The worker is the home of Programs, and it has a backend for each axis:

| Backend | Interface | Responsibility | Built-in implementation |
|---|---|---|---|
| **Storage** | `program.Backend` | Program content List/Read/Upsert/Write/Edit/Remove | `pgbackend` (filesystem: a directory per Program, `PROGRAM.md`/`SKILL.md` entry, YAML frontmatter + body) |
| **Execution** | `program.ScriptProgramExecutor` | Run a `FormType:script` program in an isolated sandbox | `core/impl/scriptexec/bun` (local Bun sandbox + embedded launcher) |

Both are swappable: a remote object store can back storage, and a local/remote
sandbox can back execution, without the worker or the scripts changing.

## How a script program runs

A script only ever reaches the world through a **ToolCaller**: the worker
relays each `sdk.call(target, event, arguments)` the script makes as a normal
bus `request{target,event,arguments}` and returns the `request.completed/failed`
result. The sandbox never holds a bus channel or credentials.

```
LLM/peer ── program.run{name|source, args} ──▶ program worker
  worker: load source (storage backend) → executor.Start(src, args, callTool)
    executor: spawn sandbox; embedded launcher compiles the script in memory and
              injects a minimal SDK (call/log/result/args); stdout = protocol JSONL
    script.call(target,event,args) ──▶ launcher ──▶ executor.handleCall
        ──▶ worker.callTool: mint request id, send request event to target
        ◀── request.completed/failed ── resolveReply ── call_result ──▶ script
    script.result(payload) ──▶ executor.Result() ──▶ program.run request.completed
```

`program.run` takes **exactly one** of two modes:

- `name` — run a stored script program (its entry content is the source); reuse.
- `source` — run inline TypeScript directly, nothing persisted; ad-hoc.

Plus optional `args` (exposed to the script as `sdk.args`) and
`timeout_seconds` (default 120s; the sandbox is killed on expiry).

> **Exposure / ACL note.** The relayed calls carry the target's own event type
> (`bash`, ...), which is dynamic, so the program worker's `PublishAllow`
> includes `*`. The executor is the single controlled exit — a target
> allow-list belongs there, not in the bus ACL.

## Files

| File | Responsibility |
|---|---|
| `worker.go` | `Worker` (embeds `baseworker.BaseWorker`) + `Config` (`ID` / `Bus` / `Backend` / `Executor`) + lifecycle (`Start`/`Stop`/`Snapshot`/`Restore`) + the event loop (`watch`/`process`) + Program lookup over the storage backend (`search` / `programForPath` / `matchProgram`) |
| `tools.go` | the tool event types (`search` / `load` / `edit` / `upsert` / `write` / `delete` / `run`) + `registerExtensions` + the per-tool handlers (`handleSearch` / `handleLoad` / `handleEdit` / `handleUpsert` / `handleWrite` / `handleDelete`) |
| `execute.go` | script-program execution: `handleRun` (both modes, timeout, log forwarding) + `callTool` (the `ToolCaller` implementation — mint id, send `request`, await reply) + `resolveReply` (route `request.*` replies to the waiting call) |
| `pgbackend/` | the filesystem storage backend |

Related, outside this package:

| Location | Responsibility |
|---|---|
| `core/itfs/program` | the contracts: `Program` / `Meta` / `ContentType` / `FormType`, the storage `Backend`, and the execution `ScriptProgramExecutor` / `ScriptSession` / `ToolCaller` |
| `core/impl/scriptexec/bun` | the local Bun-sandbox executor (`bun run --sandbox --offline`) + the embedded `launcher.mjs` (trusted bootstrap: in-memory compile + minimal SDK injection) |

## The script SDK

Inside a run the script is handed a minimal `sdk`:

- `sdk.call(target, event, arguments)` → `Promise<result>` — invoke a capability
  on another worker (a bus request).
- `sdk.log(text)` — a log line (forwarded to the worker log).
- `sdk.result(payload)` — the run's final result (or `return` a value).
- `sdk.args` — the `program.run` `args`.

The script body is compiled in memory as an async function body: top-level
`await` works, `import` does not (so it cannot reach `fs`/`net`); the Bun
sandbox is the system-level backstop.

## Relationship to the reason worker

A reason worker discovers the program worker's tools (e.g. `program__search`,
`program__load`, `program__run`) like any peer's capabilities. `program.run`
lets the model execute a whole encoded sequence of tool calls in **one** tool
call instead of one reasoning round per call — the round-compression PTC
provides. The reason worker's own program tools (`program_query` /
`program_update`) manage *its* program list; content and execution live here.
