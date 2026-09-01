# wsbackend

wsbackend is niq's workspace backend abstraction layer (under pkg/services/). It defines **interface contracts** for filesystem operations, command execution, and file search, and provides an **embedded local implementation** along with **pure helper functions**.

The workspace worker consumes wsbackend through interface assertions and never imports concrete implementations.

## Architecture

```
worker/workspace/worker.go          ← consumer, only depends on backend.go interfaces
        │
wsbackend/
├── backend.go    ← interface contracts + shared types (CommandResult, etc.)
├── embedded.go   ← the built-in implementation: local filesystem + os/exec
└── helper.go     ← pure utilities (FormatRead, FormatBash, NormalizeQuotes, etc.)
```

## File responsibilities

### backend.go — interface contracts

| Interface | Purpose |
|---|---|
| `FileOperator` | Read / Write / Edit |
| `BashOperator` | Bash + BashStream (streaming output) |
| `DirLister` | List (directory listing) |
| `GrepOperator` | Grep (recursive pattern search) |
| `FindOperator` | Find (filename glob matching) |

Also defines `CommandResult` and `WithOnUpdate` / `OnUpdate` (streaming callback context helpers).

### embedded.go — local implementation

`EmbeddedBackend` implements all contracts. Key features:

- **per-file mutex**: Write and Edit on the same file are serialised; different files proceed concurrently
- **path safety**: `resolvePath` prevents `../` and symlink escapes from the root directory
- **fuzzy fallback**: when an Edit `old_string` is not found, the backend retries with Unicode quote normalisation (curly quotes → straight, em-dashes → `--`)
- **bounded streaming bash**: `BashStream`/`Bash` run each command in its own process group, capture output through a head+tail bounded buffer (never the full stream), and kill the whole group when the combined output exceeds `BashLimits` (bytes/lines) or the context is cancelled. Oversized output is truncated to head+tail with the omitted middle marked inline.

### helper.go — pure utilities

Zero-dependency functions shared by `embedded.go` and the workspace worker:

| Function | Purpose |
|---|---|
| `FormatRead` | line numbering + offset/limit pagination + truncation |
| `FormatBash` | exit code + stdout/stderr formatting + truncation guidance note |
| `FormatLs` | `[dir]` / `[file]` structured summary |
| `NormalizeQuotes` | Unicode quotes → ASCII (fuzzy edit fallback) |
| `ShellQuote` | safe shell argument quoting |
| `TruncateOutput` | output truncation with line/byte limits |
| `GetIntArg` | typed argument extraction from tool args map |

## Design principles

1. **Interfaces are contracts, not implementations** — `backend.go` declares what backends can do, not how
2. **Worker never imports concrete backends** — the worker uses interface assertions; switching to a `DockerBackend` requires only a `Config` change
3. **Helper never imports backend** — `helper.go` is pure functions, no import cycles
4. **Locks belong to the implementation** — per-file mutexes are an internal concern of `EmbeddedBackend`; interfaces make no concurrency guarantees
