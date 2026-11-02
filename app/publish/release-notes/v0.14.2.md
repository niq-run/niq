# niq v0.14.2

A first-run and reliability patch on top of v0.14.1. A fresh install now leads
you into a model provider instead of dying at startup, a project that refuses to
start says why, its page stays usable while it is stopped, and `make install`
builds the UI it embeds.

## Highlights

- **First run has somewhere to go.** When no provider is configured, the
  control plane seeds a deepseek template, says so in a banner, and opens the
  providers panel on that editable card — with a warning and a way out if
  `provider.json` cannot be parsed.
- **A failed start reports the reason.** The start call answers with the child's
  exit, the project log path and the log's last lines; the reason is written to
  that log instead of only stderr; the WebUI prints the server's message.
- **A stopped project keeps its page.** Its address loads the product UI again —
  talk, events, workers — with the header's "project stopped — Start" banner,
  instead of redirecting to the projects list.
- **An empty talk selection means no worker filter.** It used to expand to every
  talk partner, one `worker IN (…)` branch with three bound variables each,
  which SQLite refused on projects with a large roster.
- **`make build` / `make install` build the WebUI first**, so the binary never
  embeds a stale UI. The step prints what it is doing, repairs an interrupted
  dependency install, respects the machine's npm registry, and the version line
  now carries the build time.

## 1. First run: a provider to fill in

A project with an LLM worker cannot start without a provider, and a new machine
has none — which used to end in `no LLM provider configured: edit … and re-run`
with an empty providers panel.

- The built-in template is one `deepseek` provider with an empty `api_key`, so
  the only thing missing is the key. `EnsureExample` seeds it on disk, and
  `GET /api/providers` answers with it when the file does not exist yet
  (read-only: the file appears when you save).
- The control plane shows a setup banner while no provider is usable — nothing
  configured, every `api_key` empty, or `provider.json` unreadable — and jumps
  to the providers panel from it. It re-checks whenever the panels close, so
  saving a key clears it.
- An unparsable `provider.json` no longer leaves a blank panel: it falls back to
  an empty config with a warning that saving replaces the file.

## 2. A failed start says why

`launchProject` waited for the new instance's WebUI port and then answered 200
with `webui_port: 0` when the process had already died, so the projects list,
the sidebar menu and the banner had nothing to report.

- The start now watches the child's exit alongside its port. An early exit
  answers 502 with `project <id> exited during startup: <err>`, the log path and
  the log's last four lines; a timeout reports the elapsed budget.
- `niq project run` logs its fatal startup error (`[project] startup failed: …`)
  before returning, so the log the message points at actually contains it.
- The SPA throws the server's message for start and restart instead of
  `start failed: 502`, and renders it with `pre-wrap` so the log tail keeps its
  lines — on the projects list, in the stopped banner and in the sidebar menu.

## 3. A stopped project keeps its page

A stopped project's `/p/<id>/` redirected to the projects list, which made its
own views unreachable — and with them the header banner that offers Start.

- The control plane serves the embedded SPA for navigation and for the bundle's
  static files under the project prefix while the project's API calls keep their
  plain 502. The SPA takes the project id from the URL when `/api/context` is
  unreachable, skips the event stream (and its "stream dropped" banner) while the
  project is known stopped, and shows the stopped banner with Start.
- The `?start=` handoff and the projects-list start dialog are gone with the
  redirect they existed for.

## 4. Talk scope

With nothing selected in the sidebar, the talk view expanded its scope to every
talk-partner worker and sent them all to the event APIs. Each worker becomes a
`worker IN (…)` branch with three bound variables chained by `UNION`, so a large
roster produced a query SQLite rejected and the stream failed.

An empty selection now sends no `worker` param at all: the stream is unfiltered
and `TalkView` narrows it client-side by worker type, which is what it already
did before the server-side scoping was introduced. Picking workers keeps the
server-side filter. Unfiltered mode shares the newest-N trim window with
non-conversation events, so a busy project may page once more to fill the list.

## 5. Building and version

- `make build` (and `make install`, which depends on it) runs the webui target
  first: `tsc -b && vite build`, then `go build`. The binary can no longer embed
  whatever UI happened to be committed.
- Dependencies are keyed on npm's completion marker
  (`node_modules/.package-lock.json`), with `package-lock.json` as prerequisite:
  an interrupted install or a changed dependency set reinstalls, a current tree
  is reused. `--include=dev` keeps `tsc` and `vite` on machines that export
  `NODE_ENV=production`.
- Every stage announces itself, and `npm ci` logs each fetch so a slow mirror
  shows which host it is waiting on (`make build NPM_LOGLEVEL=notice` quiets it).
- `app/webui/assets/.npmrc` sets `replace-registry-host=always`: the lockfile
  records npmmirror tarball URLs, and npm's default only rewrites npmjs hosts,
  which pinned other machines to that mirror.
- The identity line gained a build timestamp injected by the Makefile
  (`-X main.buildTime`) and by goreleaser (`{{.Date}}`). A plain `go build`
  omits it:

```
 - niq -  version: 0.14.2 · build: 2026-11-02T11:22:47Z
```

## Also in this release

- **`niq update`**, plus a quiet note on start when a newer release is published;
  the registry is asked at most once a day.
- **Collapsible sidebar sections**, with the same spacing whether open or closed.
- **Provider cards collapse**, one row each.
- **The approvals and management panels lose their worker selector**, which did
  not apply there.
- **The recipient picker shows its selection as a chip strip** and keeps the
  search row pinned.
- **MCP is "MCP workers"** in the navigation, and the client is never handed a
  null event list.

## Upgrade

Install the new binary — `git pull && make install` for a source install,
`niq update` for an npm install — then bring the control plane and every project
onto it:

```
niq restart -p
```

Projects keep serving the page they were built with until they are restarted —
`-p` is what upgrades them alongside the control plane.

## Verification

`go build ./...`, `go vet ./app/...` and `go test ./app/...` pass, with new
tests covering the stopped project's shell and static files versus its 502 API,
`waitProjectStart` reporting an early exit with the log path, and the log tail.
The SPA builds with `tsc -b && vite build`; the bundle is rebuilt and committed.
`make build` and `make install` were exercised end to end, together with all
three `npm ci` guard states (missing marker, stale marker, current marker) in a
scratch tree.
