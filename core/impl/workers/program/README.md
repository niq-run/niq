# program — the Program worker

`program` is niq's domain-service worker for **Programs** — the reusable
"source" a worker loads and that compiles into tool calls. It manages a
Program's lifecycle (search / read / write / edit / touch / update / delete)
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
| **Backend (whole environment)** | `program.ProgramBackend` = `program.Store` + `program.Executor` | One value to swap: where content lives and how a script program runs | `program.Compose(pgbackend, quickjsexec)` |
| Store half | `program.Store` | Program content List/Read/Upsert/Write/Edit/Remove | `pgbackend` (filesystem: a directory per Program, `PROGRAM.md`/`SKILL.md` entry, YAML frontmatter + body) |
| Executor half | `program.Executor` | Run a `FormType:script` program | `quickjsexec` (in-process QuickJS VM per session) |

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
    executor: new VM; evaluate the embedded prelude (the SDK facade), then the
              script as an async function body; drive its jobs until it settles
    script.call(target,event,args) ──▶ prelude ──▶ __niq_send ──▶ executor relay
        ──▶ worker.callTool: mint request id, send request event to target
        ◀── request.completed/failed ── resolveReply ── settle promise ──▶ script
    script.result(payload) ──▶ executor.Result() ──▶ program.run request.completed
```

`program.run` takes **exactly one** of two modes:

- `path` — run a stored script program (its entry content is the source); reuse.
  `name` is still accepted as a legacy alias.
- `source` — run inline JavaScript directly, nothing persisted; ad-hoc.

Plus optional `args` (exposed to the script as `sdk.args`) and
`timeout_seconds` (default 120s; the VM is interrupted on expiry).

> **Exposure / ACL note.** The relayed calls carry the target's own event type
> (`bash`, ...), which is dynamic, so the program worker's `PublishAllow`
> includes `*`. The executor is the single controlled exit — a target
> allow-list belongs there, not in the bus ACL.

## Files

| File | Responsibility |
|---|---|
| `worker.go` | `Worker` (embeds `baseworker.BaseWorker`) + `Config` (`ID` / `Bus` / `Backend` / `Executor`) + lifecycle (`Start`/`Stop`/`Snapshot`/`Restore`) + the event loop (`watch`/`process`) + Program lookup over the storage backend (`search` / `programForPath` / `matchProgram`) |
| `tools.go` | the tool event types (`search` / `read` / `edit` / `touch` / `update` / `write` / `delete` / `run`) + `registerExtensions` + the per-tool handlers (`handleSearch` / `handleRead` / `handleEdit` / `handleTouch` / `handleUpdate` / `handleWrite` / `handleDelete`) |
| `execute.go` | script-program execution and its reply shaping: `handleRun` (both modes, timeout, log collection), `callTool`/`relay` (the `ToolCaller` — mint id, send `request`, await reply), `resolveReply` (route `request.*` replies to the waiting call), `checkTarget` (ask the directory worker), and the `sdk.log` tail buffer + 20 KiB result cap that decide what the reply text looks like |
| `pgbackend/` | the filesystem store (one `program.Store` implementation) |
| `quickjsexec/` | the in-process executor (one `program.Executor` implementation): `executor.go` (options + `Start`), `session.go` (the VM's owner goroutine, the job loop, host functions, cancellation), `errors.go` (line mapping from `<eval>` frames), `prelude.js` (the embedded SDK facade) |

Related, outside this package:

| Location | Responsibility |
|---|---|
| `core/itfs/program` | the contracts: `Program` / `Meta` / `ContentType` / `FormType`, the `ProgramBackend` environment with its `Store` and `Executor` halves, plus `ScriptSession` / `ToolCaller` |

## The script SDK

A script runs in a fresh in-process QuickJS VM (no subprocess, no `node`) and
may only reach the world through `sdk`. **Sandbox contract:** the engine is not
a module system — `import`, `export`, dynamic `import(...)` and `require` do not
exist — and neither do filesystem, process or network globals (`process`,
`fetch`, `Deno`/`Bun` globals, sockets). Anything that needs files, a shell,
environment variables or the network goes through another worker via
`sdk.call` (e.g. the workspace worker's `bash`).

Inside a run the script is handed a minimal `sdk`:

- `sdk.message(target, text)` — a fire-and-forget note: sends a `worker.input` (append
  mode) and resolves once it is on the bus. Nothing comes back from the target — the
  script-side twin of the `message` tool, so all three built-in sends survive into a
  script instead of `message` decaying into `call(target, "worker.input", …)`.
- `sdk.call(target, event, args)` — a normal request to another worker; resolves with the
  reply VALUE, type preserved: a prose reply is a string, a structured reply is a real
  JS object/array (no `JSON.parse`), because the worker sends `result` as-is rather than
  encoded (a tool-level failure is a result, not an exception) and rejects on unknown
  worker / failed / rejected. Concurrent (`Promise.all` fans out). An unknown target fails fast:
  the roster is the directory worker's (it already collects `worker.ready` /
  `worker.gone` for the fleet), so the program worker asks it — no second copy to drift.
  An unreachable directory degrades to "cannot tell" (the call proceeds, bounded by the
  run timeout) instead of blocking.
- `sdk.log(text)` — a log line. Mirrored to the worker log AND returned to the caller
  after the result under a `--- script log ---` header (last 100 lines / 8 KiB,
  with a dropped-lines note), so a failing run is debuggable in the same round.
- `sdk.result(payload)` — the run's output, kept as the value it is (string,
  object, array …); the last call wins, and a top-level `return` is used only when
  `sdk.result` was never called (neither → the empty string).
- `sdk.args` — the `program.run` `args` (always an object, defaults to `{}`).
- `console.log/info/warn/error/debug` — the engine has no console; the prelude
  maps it onto `sdk.log`, so a stray console call shows up in the run's log
  instead of throwing.

The reply of `program.run` is an OBJECT (so it travels as the payload itself):

```json
{ "output": <the script's return value, any JSON type>,
  "log": ["line", "…"],
  "log_truncated": 3 }
```

`output` keeps the script's type (a string stays a string, an object stays an
object) and is capped at 20 KiB — a longer string is elided head+tail, an
oversized structured value degrades to a truncation note. `log` is omitted when
the script logged nothing, `log_truncated` only when older lines were dropped.
A failed run replies `request.failed` with the error plus the log collected so
far, which is where the cause usually is. A script error names the failing line
of the user's own source.

The old text reply was the result plus — when the script logged — a `--- script log ---`
section.

The script body is appended to the embedded prelude as the body of an async
arrow function, so top-level `await` and a top-level `return` both work. The
VM's globals are only what the prelude installs (`sdk`, `console`, and the
standard JavaScript objects), which makes the contract above a property of the
environment rather than a rule a script is asked to follow. **It is still not a
security boundary**: the engine is a large C program compiled to Go and living
in niq's address space, so a bug in it is a bug in niq. What it does bound is
misuse — a per-run `SetMemoryLimit`, a call-depth limit, and a watchdog that
interrupts a script which never yields. A harder boundary is a different
Executor: the wazero/QuickJS wasm runtime, a container, or a remote execution
backend.

## Relationship to the reason worker

A reason worker discovers the program worker's tools (e.g. `program__search`,
`program__read`, `program__run`) like any peer's capabilities. `program.run`
lets the model execute a whole encoded sequence of tool calls in **one** tool
call instead of one reasoning round per call — the round-compression PTC
provides. The reason worker's own program tools (`program_query` /
`program_attach` / `program_detach`) manage *its* program list; content and
execution live here.
