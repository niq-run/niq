# niq v0.12.0

A WebUI release: the talk view now shows the worker meta capabilities instead of
hiding them, worker permissions are edited through one shared module with proper
pickers and backend defaults, and the project label in the corner became an
interactive wordmark. One bus-level cleanup rides along: "any source" gained a
canonical spelling.

## Highlights

- **`worker.help`, `worker.readme` and `worker.readme.update` render in the talk
  view** — live and, thanks to one shared keep-list, after a refresh too.
- **Readmes are Markdown.** Every place that describes the readme — the meta
  tool descriptions, the `readme` parameter, the reason system prompt and the
  host spawn guide — now asks for short Markdown, because the WebUI renders it
  for human readers.
- **Permissions are edited in one place.** The create dialog and the worker
  detail tab share a collapsed publish/subscribe module, with event-type pickers
  and each worker type's backend defaults pre-filled.
- **"Any source" is spelled `*`.** An omitted subscription source is normalized
  to the canonical wildcard on read and on write; the empty form stays accepted.
- **The project label is interactive.** Left-click cross-fades between the niq
  wordmark and the project name; the wordmark supports drag, a right-click pin
  flip and a palette toggle.

## 1. Worker meta capabilities in the talk view

`worker.help` and `worker.readme` used to reach the browser but be dropped by the
`worker.*` filter, so their invocations never appeared in the timeline. Both now
render as normal tool cards, request and `request.completed` included.

The keep-list was the subtle part. The live SSE stream had a `keep` override
while the watermark backfill and the load-more pagination kept only
`worker.input` and `worker.abort`, so a meta call appeared live and disappeared
on refresh. A single module-level `TALK_KEEP` now feeds the stream and both
history queries.

- `worker.readme.update` joins the list too, so a readme revision stays visible.
- The detail panel's own readme fetch (HIW → worker) is still filtered out —
  that is UI housekeeping, not a conversation row. An agent reading a peer's
  readme is kept.
- Worker selector rows show a hover `i` control immediately after the worker
  name. Clicking it opens the readme tab without toggling selection or starting
  a drag; it does the same thing as the context menu entry.

## 2. Readmes are Markdown

The `worker.readme` and `worker.readme.update` descriptions, the `readme`
parameter, the reason worker's `readme_update` instruction and the host spawn
guide now recommend Markdown instead of plain prose. Nothing is enforced: an
existing plain-text readme still reads fine, and the format only affects how it
renders in the WebUI.

## 3. Worker permissions and forms

- **One permissions module.** Subscriptions and publish grants move into a
  collapsed module shared by the create dialog and the worker detail tab, so the
  two forms can no longer drift.
- **Drafts open on the backend defaults** for each worker type.
- **Event-type pickers.** Publish rows keep free text (a publish grant may target
  an event no worker currently sends); subscribe rows pick their event from the
  chosen source worker's own publish grants.
- **Environment variables** are key/value rows instead of a `KEY=value` textarea.
- **The remote type** keeps `remote-niw` as its only preset.
- **Workers view** gains a free-text search box alongside the type/tag/status
  filters.
- **Lifecycle buttons** in the detail panel no longer stretch to full width and
  report an explicit in-flight → done status that stays until the next action
  instead of vanishing after a few seconds. The lifecycle POSTs are bounded by a
  30s timeout.

## 4. "Any source" is spelled `*`

A subscription with no source restriction had two equivalent spellings — an
absent source and the wildcard — and different code paths picked different ones.
`*` is now canonical:

- `EventPattern.UnrestrictedSource` reports whether a pattern accepts any
  source, and `Matches` treats `""` and `"*"` alike.
- `NewPattern` fills in `SourceID: "*"`.
- `SubscriptionSpec.ToPattern`, the config `subscriptions` parser and the WebUI's
  `update allow` handler normalize an omitted source to `"*"` on read and on the
  registry write path.
- The empty form remains accepted on input, so older configs and existing
  registry entries keep working without migration.

## 5. WebUI chrome

- **Corner label.** Left-clicking the logo cross-fades between the niq wordmark
  and the project name; right-click renames the project. The project name uses a
  system mono stack instead of the bundled subset font, which only ships a
  partial glyph set. Drag and the pin's right-click flip apply only while the
  wordmark is shown; clicking the pin toggles the palette. When the project name
  is shown, the footer's project label becomes the accent-coloured niq wordmark.
- **Footer project menu** no longer waits for a defined control-plane status:
  restart and stop stay available with an explicit unknown-status note, and a
  stopped project still offers start.
- **MCP panel.** The per-server status dot is gone. It only reflected whether a
  live MCP session existed, so it looked "off" for servers whose tools listed
  fine, and it stayed stale until a refresh. The status text and tool count
  remain.

## Upgrade notes

- **Subscription output spelling changes.** Any-source subscriptions are now
  written as `"*"` in config and in `GET`/`PUT` allow responses. Consumers that
  compare against an omitted source should treat both forms as equivalent.
- **Go API.** `EventPattern.UnrestrictedSource` is new, and `event.NewPattern`
  now sets `SourceID: "*"`. Pattern matching is unchanged for every input, so
  existing workers need no code changes.
- **No TypeScript SDK change.** `@niq.run/worker-sdk` stays at 0.8.0; nothing in
  this release touches it.
- **No event contracts changed.** The readme/help work here is descriptions and
  rendering only.

## Validation

- `go build ./...` and `go test ./...` clean on the release tree.
- WebUI production bundle rebuilt from source and committed
  (`npm --prefix app/webui/assets run build`).
- Release binaries for darwin / linux / windows on amd64 + arm64, with
  `niq --version` reporting `0.12.0`.
