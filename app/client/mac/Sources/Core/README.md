# `Sources/Core` — wire models, gateway client, CLI locator (no UI)

The framework-agnostic half of the client. Everything here imports **Foundation
only** (no SwiftUI/AppKit), so it can be unit-tested and, later, lifted into its
own module.

| File | Responsibility |
|---|---|
| `Models.swift` | The wire shapes: `ProjectView`, `Worker` (+ placement / lifecycle / tags / params / credential / allow lists), `Event` (+ scope / time / tool semantics), `Block`, `InputBody`, `Ports`, `UsageInfo` (window / tokens / cache / grouped `breakdown`), `ApprovalEntry` / `ApprovalDecision`, provider types, `ProgramInfo` / `ProgramDetail` / `ProgramContent`, `MountList` / `WorkerContextStatus`, `WatchEntry` / `PublishPattern` / `EventPattern` / `AskReply`, `StagedAttachment` / `UploadedFile`, and `JSONValue` (payloads stay open, with pretty-printing + a `JSONSerialization` bridge). Mirrors `app/standalone/contract`. |
| `GatewayClient.swift` | HTTP to the gateway. Control plane: `listProjects` / `start` / `stop` / `restart` / `createProject` / `linkProject` / `projectDirs`, `templates` (list / get / create / update / delete / import), `providersRaw` / `updateProviders`. Project data port: `workers` / `history` / `sendInput` / `abort` / `stream` (SSE, reports the watermark), `workerProviders` / `setWorkerProvider` / `workerAction` / `setWorkerArchived` / `deleteWorker` / `createWorker` / `updateWorkerMeta` / `updateWorkerParams` / `updateWorkerAllow` / `workerContext` / `workerMounts` / `mutateMount`, `approvals` / `decideApproval`, `programs` / `program` / `programContent` / `updateProgram` / `deleteProgram`, `upload`, and the **bus `ask`** (publish a worker event, then poll `events/by-request` for the terminal reply — how readme / MCP / context / mounts are driven). |
| `GatewayDiscovery.swift` | Finds the gateway address from `<niq root>/shell.json` when the `niq` binary is not on PATH. |
| `NiqCLI.swift` | Finds the `niq` binary and runs `niq status --json` / starts-stops the gateway — the "third-party client drives the CLI" path from the client doc. |

## Rules

- No UI types. No SwiftUI import.
- Base URLs come from `NiqCLI.status()` (the gateway's own `url`); a client
  never hardcodes a project's port.
- The SSE parser skips heartbeats (`:`) and the watermark control frame, and
  decodes only frames that are an `Event`.
