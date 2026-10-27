# niq v0.11.0

A feature release built around how agents find and use each other. Every
built-in worker can now describe itself and explain one of its events on demand;
the WebUI addresses messages explicitly; MCP workers get a dedicated management
view; and a new project starts with a playbook that teaches it the fleet.

## Highlights

- **Every built-in worker serves `worker.readme`**, and a reason readme now says
  what the worker is responsible for *and* what it is working on now.
- **`worker.help` explains one event at a time** — a CLI's `-h` for the bus.
  Omit `event_name` for an index, pass it for a guide. Host adds a
  worker-type-specific spawn guide; mcp adds static guides for its server/tool
  events.
- **`@` addressing is explicit and multi-target.** `POST /api/input` takes
  `targets`, every addressed worker receives the same event, and an empty target
  set is refused instead of broadcasting.
- **A dedicated MCP view** manages servers, exposure and allowlists, routing
  edits over the bus for live workers and through the declaration for fixed or
  offline ones.
- **The default template bootstraps itself**: the `using-niq` playbook and the
  history worker ship with it.
- **`@niq.run/worker-sdk` 0.8.0** mirrors the two meta capabilities.

## 1. `worker.readme`: duties and current focus

`worker.readme` is now served by every built-in worker: reason / coding,
directory, history, host, mcp, program, timer, workspace, niw and hiw. The guide
covers what the worker is for, which events it serves, and — where the worker
has live state — the current mounts, timers, servers, bindings or approval
queue.

A declaration can customize the text without code changes:

- `readme` replaces the built-in guide;
- `readme_append` appends to it;
- both render `{{key}}` placeholders from the declaration params.

The reason readme changed meaning slightly: it is no longer only an identity
card. Peers read it right before delegating, so it now states (1) what the
worker is responsible for and (2) what it has recently been working on. The
system prompt tells the worker to refresh the second part as its focus moves,
and to change the first only when its role does.

## 2. `worker.help`: per-event help

`worker.help` is the per-event counterpart of `worker.readme`:

```json
{"event_name": "spawn", "params": {"worker": "reason"}}
```

- Omitting `event_name` returns an index of every event the worker serves.
- A named event renders the generic guide built from its declared description,
  parameters and response — enough for a caller to plan the payload before
  calling.
- `params` is an optional selector; a worker-specific guide may use it to return
  a more precise answer.
- An unknown event is answered with `request.failed`, naming the events the
  worker actually serves.
- `Internal` capabilities appear only in their owner's index, matching the
  dispatch scope rule.

Custom guides in this release:

- **host** — the spawn guide. `{"event_name":"spawn","params":{"worker":"reason"}}`
  documents every reason-family construction parameter (`goal`, `brief`,
  `programs`, `subscriptions`, model overrides, context budget, display
  metadata); without the selector it returns the general spawn reference.
- **mcp** — static guides for `mcp.server.list`, `mcp.server.add`,
  `mcp.server.remove`, `mcp.server.update`, `mcp.tool.list`, `mcp.tool.call` and
  `mcp.expose`. The way a fixed MCP event is used does not depend on which
  server or tool is involved, so `params` never changes these guides. Ready-mode
  flattened tools (`mcp.<server>.<tool>`) fall back to the contract-derived
  guide, which carries the remote tool's own description, input schema and
  output schema.

`worker.help` is opt-in: a worker calls `BaseWorker.RegisterHelp`. Host and mcp
serve it in this release; the base implementation gives every registered event a
usable guide even without custom text.

## 3. Addressing and messages

- **Explicit `@` addressing.** The recipients of a message are the workers it
  mentions plus the target set. A send that resolves to nobody is refused: Send
  says why on hover, and the recipient chip flashes a hint.
- **Multi-target delivery.** `POST /api/input` takes `targets` and delivers the
  same `worker.input` event to each; the bus records the full recipient list. An
  empty target list is refused rather than falling through to a broadcast. The
  old `target` spelling is still honoured for older clients.
- **The target set is its own state.** The sidebar selector seeds it; the
  recipient picker edits only the target; typing an `@` appends to it; and a
  successful send keeps the resolved set for the next message.
- **Mention picker** ranks an exact worker id first, drops tag grouping while
  searching, and commits the highlighted row on Enter even after the query
  narrowed the list.
- **Abort reads like the message it is** ("@worker — please stop") and stops
  every addressed worker. A multi-worker send carries a reminder that everyone
  received the same message.

## 4. MCP view

A dedicated view for mcp workers: roster with online state, declared mode
(fixed/dynamic) and server count, plus a detail panel with live server status,
protocol, URL and tool count.

- A dedicated create form composes several servers, mode, exposure and an
  allowlist.
- Edits are explicit (draft → save): a live dynamic worker replays the diff over
  the bus (`remove` → `add` → `update` → `expose`); a fixed or offline worker has
  its declaration params rewritten and is restarted.
- The tool list renders the declared output schema next to the input schema.
- Local (stdio) MCP is explicitly not offered yet.

## 5. Default template and UI correctness

- **Default template.** It now ships the `using-niq` playbook (full guide plus a
  one-page cheat sheet) and the history worker, so a fresh agent can discover
  peers, talk to them, split focus and recall the past without help. Template
  `programs/` fall back to the embedded built-in when the templates directory has
  not been seeded, so CLI-created and WebUI-cloned projects keep them.
- **Structured tool replies render in the talk view.** An object reply is the
  payload itself; the talk view now falls back to the payload when there is no
  `result`, and summarizes multi-modal content instead of dumping base64.
- **Event-type tags colour by outcome**, not family: failures red, completions
  green, request-ish types yellow, reason purple, everything else neutral.
- **Navigation** splits into project views (talk / events / approvals) and
  project resources (workers / MCP / programs). The worker detail drawer opens at
  80%, like the program drawer, and a trace id in event detail links to that
  trace's events.
- **`program.run` sandbox contract** is now spelled out in the tool description,
  the coding worker's PTC instruction and the program README: no imports, no
  filesystem/process/network APIs; everything host-facing goes through
  `sdk.call`. This is prompt-level guidance, not yet enforced.

## TypeScript SDK

`@niq.run/worker-sdk` **0.8.0** is published with the new capabilities:

- `BaseWorker.registerReadme` and `BaseWorker.registerHelp`, mirroring the Go
  semantics (readme override/append/`{{key}}`; help index, contract-derived
  fallback, custom selectors, internal visibility).
- `EventType.WorkerReadme`, `EventType.WorkerHelp`, and the exported `HelpFunc`
  type.

## Upgrade notes

- **TypeScript workers**: upgrade to 0.8.0 explicitly — a `^0.7.0` range will not
  pick it up. The change is additive; existing workers need no code changes.
- **`POST /api/input`** now rejects an empty `targets` list. Any client that used
  to send a message without addressing anyone should name at least one worker.
  The legacy `target` field still works.
- **`worker.help` is a new universal event.** It is opt-in per worker and does
  not change any existing event or Go signature.
- Built-in workers now answer `worker.readme`; peers and the WebUI may call it
  where they previously saw no capability.

## Validation

- `go build ./...`, `go test ./...` clean.
- `sdk/ts`: `npm test` (57 tests), `npm run typecheck` and `npm run build` green;
  0.8.0 published to npm.
- Release binaries for darwin / linux / windows on amd64 + arm64, with
  `niq --version` reporting `0.11.0`.
