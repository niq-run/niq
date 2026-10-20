# program — the Program worker

`program` is niq's domain-service worker for **Programs** — the reusable
"source" a worker loads and that compiles into tool calls. It manages a
Program's lifecycle (search / load / write / edit / upsert / delete)
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
| **Backend (whole environment)** | `program.ProgramBackend` = `program.Store` + `program.Executor` | One value to swap: where content lives and how a script program runs | `program.Compose(pgbackend, jsexec)` |
| Store half | `program.Store` | Program content List/Read/Upsert/Write/Edit/Remove | `pgbackend` (filesystem: a directory per Program, `PROGRAM.md`/`SKILL.md` entry, YAML frontmatter + body) |
| Executor half | `program.Executor` | Run a `FormType:script` program in a separate process | `jsexec` (local `node` + embedded launcher) |

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

- `path` — run a stored script program (its entry content is the source); reuse.
  `name` is still accepted as a legacy alias.
- `source` — run inline JavaScript directly, nothing persisted; ad-hoc.

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
| `execute.go` | script-program execution and its reply shaping: `handleRun` (both modes, timeout, log collection), `callTool`/`relay` (the `ToolCaller` — mint id, send `request`, await reply), `resolveReply` (route `request.*` replies to the waiting call), `checkTarget` (ask the directory worker), and the `sdk.log` tail buffer + 20 KiB result cap that decide what the reply text looks like |
| `pgbackend/` | the filesystem store (one `program.Store` implementation) |
| `jsexec/` | the local subprocess executor (one `program.Executor` implementation; `node` by default, `script_executor_args` to change it) + the embedded `launcher.mjs` (trusted bootstrap: in-memory compile, minimal SDK injection, error-location mapping) |

Related, outside this package:

| Location | Responsibility |
|---|---|
| `core/itfs/program` | the contracts: `Program` / `Meta` / `ContentType` / `FormType`, the `ProgramBackend` environment with its `Store` and `Executor` halves, plus `ScriptSession` / `ToolCaller` |

## The script SDK

Inside a run the script is handed a minimal `sdk`:

- `sdk.call(target, event, args)` — a normal request to another worker; resolves with the
  target's reply TEXT (a tool-level failure is a result, not an exception), rejects on
  unknown worker / failed / rejected. Concurrent (`Promise.all` fans out). An unknown
  target fails fast: the roster is the directory worker's (it already collects
  `worker.ready` / `worker.gone` for the fleet), so the program worker asks it —
  no second copy to drift. An unreachable directory degrades to "cannot tell"
  (the call proceeds, bounded by the run timeout) instead of blocking.
- `sdk.log(text)` — a log line. Mirrored to the worker log AND returned to the caller
  after the result under a `--- script log ---` header (last 100 lines / 8 KiB,
  with a dropped-lines note), so a failing run is debuggable in the same round.
- `sdk.result(payload)` — the run's final result: a string verbatim, anything
  else JSON-encoded; the last call wins, and a top-level `return` is used only
  when `sdk.result` was never called (neither → the empty string).
- `sdk.args` — the `program.run` `args` (always an object, defaults to `{}`).

The reply is the result, plus — when the script logged — a `--- script log ---`
section. The result is capped at 20 KiB (head + tail, with an elided-bytes
note); `timeout_seconds` (default 120) bounds the whole run including nested
calls, and on expiry the log collected so far comes back with the error. A
script error is reported with the failing line of the user's own source.

The script body is compiled in memory as an async function body: top-level
`await` works and static `import` syntax does not parse, so a normal script
cannot pull in modules. **This is accident containment, not a security
boundary**: the script runs in a plain `node` process, so a
runtime `await import("node:fs")` or a global like `fetch` is reachable, and it
runs as the same OS user. That is by design — the goal is to stop a script from
misbehaving by mistake, not to withstand one that attacks on purpose (scripts
are the local model's own code). Real isolation is a different Executor:
QuickJS (planned), a wrapper command via `script_executor_args` (sandbox-exec /
bwrap / container), or a remote execution backend.

## Relationship to the reason worker

A reason worker discovers the program worker's tools (e.g. `program__search`,
`program__load`, `program__run`) like any peer's capabilities. `program.run`
lets the model execute a whole encoded sequence of tool calls in **one** tool
call instead of one reasoning round per call — the round-compression PTC
provides. The reason worker's own program tools (`program_query` /
`program_update`) manage *its* program list; content and execution live here.
