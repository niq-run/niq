# workspace — File system and shell commands

Workspace Worker provides file read, write, edit, shell command execution

## Architecture

Two layers:

```
bus (tool.request / tool.completed)
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
Event{Type: "tool.request", TargetWorkerID: "ws-proj"}
  Payload: {"name": "read", "arguments": `{"path":"main.go"}`}
  → w.handlers["read"](ctx, {"path": "main.go"})
```

No prefix stripping. No string encoding of routing info in tool names.

## EmbeddedBackend

`EmbeddedBackend` is the built-in Backend implementation using Go standard library
for local filesystem and subprocess operations.

```go
ws := workspace.New(workspace.Config{
    ID: "ws-proj",
    Bus: bus,
    Backend: &workspace.EmbeddedBackend{RootDir: "/path/to/project"},
})
```

Path safety is enforced by `resolvePath()` — all paths are cleaned via
`filepath.Clean`, resolved absolutely, and checked against the root directory.

## Usage in WorkerService

`WorkerService.spawnWorkspace` creates workspace workers on demand:

```go
s.CreateWorker(id, func() worker.ManagedWorker {
    return workspace.New(workspace.Config{
        ID:      id,
        Bus:     s.Bus,
        Backend: &workspace.EmbeddedBackend{RootDir: path},
    })
})
```

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
| `backend.go` | Interface definitions (FileOperator, BashOperator) |
| `embedded.go` | EmbeddedBackend implementation |
| `worker.go` | WorkspaceWorker: bus protocol, route table, result formatting |

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
