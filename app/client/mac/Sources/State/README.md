# `Sources/State` — `AppModel` (presentation state)

One `@MainActor final class AppModel: ObservableObject` holds everything the
views read: the resolved gateway URL, the project list and selection, the worker
roster and display names, the live events, the two independent selections, the
view toggles, providers and errors.

- Built with **Combine property wrappers** (`@Published`), not `@Observable`,
  because the SwiftUI macro plugins are unavailable under CLT (see the top
  README).
- Orchestrates `Core`: `boot()` resolves the gateway and loads projects;
  `connectSelected()` loads workers (and approvals / archived) and opens the SSE
  stream; `send(text:attachments:)` posts `worker.input`; `abort()` stops the
  send targets; `projectAction` drives start/stop/restart; `createProject` /
  `linkProject` create new ones; `workerAction` / `createWorker` /
  `removeWorker` / `setArchived` manage the roster; `switchProvider`,
  `updateWorkerMeta`, `updateWorkerParams`, `updateWorkerAllow`,
  `loadWorkerContext`, `loadWorkerMounts`, `mutateMount` and `askWorker` drive
  the worker detail panel; `loadPrograms` / `saveProgram` / `removeProgram`,
  `loadTemplates` / `saveTemplate` / `importTemplate`, and
  `loadProvidersDoc` / `saveProviders` back the management surfaces;
  `loadApprovals` / `decide` the approvals queue; `loadEarlier` pages history;
  `uploadAttachment` stages composer files; `openTrace` routes 调用链 into events.
- **Two selections, kept apart:** `viewFilter` (sidebar checkboxes → which events
  the conversation shows) and `sendTargets` (composer chips → who a message goes
  to).
- View toggles mirror the WebUI: `streamingMode`, `expandThinking`,
  `collapseProcess`, `responseOnly`, plus the `appearance` override
  (system/light/dark) behind the footer's 外观 picker.
- Keeps per-worker side tables the detail panel reads: `workerProviders`,
  `workerContexts`, `workerMounts`, and `watchByWorker` (parsed from
  `worker.ready`).
- The stream runs in a `Task` stored on the model and cancelled on reselect.

No view code here; no direct URLSession — all wire calls go through
`GatewayClient`.
