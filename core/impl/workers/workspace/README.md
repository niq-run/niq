# workspace — File system and shell commands

Workspace Worker provides file read, write, edit, shell command execution

## Architecture

Two layers:

```
bus (tool event → request.completed)
  |
  ▼
WorkspaceWorker        bus protocol: subscribe, dispatch, format
  |                    capability derivation via interface probing
  ▼
Backend                pure execution, bus-unaware
                       e.g. EmbeddedBackend: os operations, sh subprocess
```

The Worker speaks the bus protocol. The Backend touches the OS.

## Backend interfaces

```go
// FileOperator — file read, write, edit
type FileOperator interface {
    Read(ctx context.Context, path string) (string, error)
    Write(ctx context.Context, path, content string) error
    Edit(ctx context.Context, path, oldStr, newStr string) error
}

// BashOperator — shell command execution, capped by BashLimits
type BashOperator interface {
    Bash(ctx context.Context, command, cwd string, limits BashLimits) (BashResult, error)
    BashStream(ctx context.Context, command, cwd string, onLine func(line string), limits BashLimits) (BashResult, error)
}

type BashLimits struct {
    MaxBytes int // combined stdout+stderr byte cap; 0 = unlimited
    MaxLines int // combined line cap; 0 = unlimited
}

type BashResult struct {
    Stdout   string
    Stderr   string
    ExitCode int

    Truncated  bool // output exceeded limits; streams are head+tail slices
    TotalBytes int
    TotalLines int
}
```

Interfaces are defined by the workspace package. The Worker probes the Backend
at Start() via type assertions and derives its capability set automatically. A
Backend can implement any subset.

## Tool registration

| Backend interface | Tools registered |
|---|---|
| `FileOperator` | `read`, `write`, `edit` |
| `BashOperator` | `bash` |

Each handler closure extracts arguments from `map[string]any`, calls the Backend
method, and formats the result into LLM-readable text. For example, `bash`
produces:

```
Exit code: 0
STDOUT:
Hello, world!
```

Non-zero exit codes are not errors — they are returned in `BashResult` and
formatted as text. The LLM receives the full context and decides how to handle
it.

## Tool routing

Routing is purely by `TargetWorkerID` on the event envelope. Tool names are bare
strings:

```
Event{Type: "read", TargetWorkerID: "ws-proj"}
  Payload: {"name": "read", "arguments": `{"path":"main.go"}`}
  → w.handlers["read"](ctx, {"path": "main.go"})
```

No prefix stripping. No string encoding of routing info in tool names.

## EmbeddedBackend

`EmbeddedBackend` is the built-in Backend implementation using Go standard library
for local filesystem and subprocess operations. It supports multiple mounted
directories: the first mount is the primary one (relative paths and the default
bash cwd resolve against it), and absolute paths are accepted when they fall
inside any mount.

```go
be := wsbackend.NewEmbeddedBackend([]string{"/path/to/project"})
ws := workspace.New(workspace.Config{
    ID:      "ws-proj",
    Bus:     bus,
    Backend: be,
    // Persistence callback: a runtime mount.add signals through this so the
    // mount set survives a restart via Snapshot/Restore.
    OnDurableChange: onCheckpoint,
})
```

Path safety is enforced by `resolve()` — all paths are cleaned via
`filepath.Clean`, resolved absolutely, and checked against the mount
boundaries (symlink-aware).

## Runtime mount management

The worker exposes `mount.add` / `mount.list` extensions (same pattern as the
reason worker's provider events). `mount.add` takes `{"path": "/abs/dir"}`,
requires the directory to exist, and rejects duplicates; on success it
notifies the durable-change callback so `Snapshot` persists the new mount
set.

## Boundary approval and read-only mode

`mount.add` is the runtime mount-update extension: requests from the
configured approver (default `webui`) apply directly; a request from any
other worker parks behind an approval — the worker sends `approval.request`
and only expands the boundary once `approval.decision` grants it, then
replies to the requester. Without a configured approver every request applies
directly.

The same approval pair covers tool escapes: when a tool path escapes every
mount and an approver is configured, the worker parks the call, sends
`approval.request`, and on approval mounts the path and re-dispatches the
call. Pending approvals travel in the worker's Snapshot.

An `approval.decision` is accepted only from the approver it was sent to (or
the current approver, after a handover); a stray decision leaves the call
parked.

### Answering a request (self-describing by design)

The approver may be an automated worker, so the request carries everything
needed to answer it — no source reading, no out-of-band docs:

- **`approval.request` payload** — the situation (`origin`, `requested_by`,
  `action`, `tool`, `path`, `exists`, `mounts`, `args`) plus a `reply` block:
  the answer event (`approval.decision`), the `target`, the `request_id`, a
  `payload_template`, what each verdict does (`approved_effect` /
  `denied_effect`) and a `hint`.
- **`approval.decision` is a registered capability** — it is announced in
  `worker.ready` with `Parameters { request_id, approved, note? }` and a
  `Response { request_id, approved, resolved, mounted }`, so a reason worker
  sees it as a callable tool (e.g. `ws-proj__approval_decision`) whose
  description explains the verdict and its effects.

Two wire forms reach the same handler:

| Form | Payload | `RequestId` | Answered? |
|---|---|---|---|
| human UI worker (protocol reply) | `{approved, note}` | the approval request | no — nobody awaits it |
| automated approver (capability call) | `{request_id, approved, note?}` | the caller's own call id | yes — `request.completed/failed` for that call |

A capability call is always answered (unknown request, wrong sender, missing
`approved`), so an automated approver's tool call cannot hang; the parked
operation itself stays put until the real approver decides. On approval the
verdict names the mount (`mounted`) in the approver's reply, while the parked
call's own reply goes to the original caller.

### Handing the approver over (`approver.set`)

The approver is seeded from config (`Config.Approver`, which the app fills
from the spawn `approver` param) and can be reassigned at runtime with
`approver.set` `{"approver": "reason-auto", "reason"?: "…"}` — e.g. to a
reason worker that decides out-of-mount requests itself, so a human no longer
approves each one. The protocol does not change: the approver is just the
address `approval.request` is sent to.

- Once an approver is configured, **only that approver may reassign it**
  (delegation), so no worker can make itself approver and self-approve.
- With **no approver configured**, any worker may install one: an empty
  approver means `mount.add` applies directly and escapes fail, so installing
  a gate only narrows what callers can do.
- An empty `approver` clears the role (back to the no-approver semantics).
- Already-parked requests keep the approver they were sent to and stay
  decidable by it, so a handover does not strand a human-held request.
- The approver, like the mount set and the mode, is durable: it travels in the
  worker's Snapshot and comes back on Restore, overriding the spawn config.

`mode.set` switches between `readwrite` (default) and `readonly` at runtime.
Tools stay registered and listening in both modes — a write/edit/bash call
while read-only is answered with an explicit error. The mode is durable too.

## Custom Backend

Implement one or more interfaces to create a custom Backend:

```go
type MyBackend struct{}
func (b *MyBackend) Read(ctx context.Context, path string) (string, error) { ... }
func (b *MyBackend) Write(ctx context.Context, path, content string) error { ... }
// Only implement what you need — the Worker probes and registers accordingly.
```

## Files

| File | Responsibility |
|---|---|
| `worker.go` | WorkspaceWorker: bus protocol, route table, result formatting, Snapshot/Restore |
| `mounts.go` | mount.add / mount.list extensions + MountManager probing |
| `approval.go` | boundary-escape → approval.request flow (with the `reply` block), approval.decision capability, parked-call resolution |
| `approver.go` | approver.set — runtime handover of the approval role |
| `mode.go` | mode.set (readwrite/readonly) extension |
| `tools.go` | tool handler assembly from backend interfaces |

## Design constraints

- **Backend is bus-unaware.** It does not import event or worker packages. It
  does not publish events or format results.
- **Tool names carry no routing info.** Routing is by TargetWorkerID on the
  envelope. Tool names are bare operation identifiers.
- **Worker formats results.** Backend returns raw data (`string`, `BashResult`,
  `[]string`). The Worker formats it into LLM-readable text.
- **Non-zero exit codes are results, not errors.** `bash` separates execution
  failure (binary not found) from exit code (process completed with non-zero
  status).
