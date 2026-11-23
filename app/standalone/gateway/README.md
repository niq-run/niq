# gateway — the one HTTP surface everything reaches

`gateway` is the only HTTP server niq exposes: every UI — the embedded SPA, the
terminal client, the macOS app — talks to the same port, so one place decides
who may open it, one rule reaches a project, and one table says what is mounted.
It is a control plane: it lists, creates, starts and stops instances but never
runs one. A project is a separate process started through `launcher` and
reached at `/p/{id}/`, which proxies only its `/api/*`; its ports live in
`project.json`, so a hand-started one is the same thing.

## Where it sits

```
client ──► gateway ──► launcher ──exec──► project process
              ├─ /              embedded UI bundle
              ├─ /api/*         control plane (projects/templates/providers)
              └─ /p/{id}/api/*  proxy ──► project's own :<webui> API
```

## Mounting a UI

A `UIMount` is a URL prefix plus an `fs.FS`; the gateway knows nothing more.
`mountFor` picks the longest matching prefix, serves the file, and falls back to
`index.html` for a browser navigation. `publish/cmd` decides what exists,
mounting `webui.Assets()` at `/` through `cli.Spec`.

## `/api/*` — the control plane

| route | answers |
|---|---|
| `GET /api/context` | `{"mode":"control"}`; this service never has a project attached |
| `/api/projects…` | list with live run state, directory picker, link, create-then-start, start, stop, restart, template preview |
| `/api/templates…` | list, raw detail, create from a copy or a body, zip import, update, delete |
| `/api/providers` | `GET` the config verbatim, `PUT` a replacement |
| `/p/{id}/…` | the project's own `/api/*`, or the mounted bundle |
| `/` | the mounted bundle |

Reads go through `launcher.List`/`ViewOf` (a `contract.View` with `running`
probed) and `template.List`; writes call `launcher.Start`/`Stop`/`Restart` and
`project.CreateProject`/`LinkProject`. `Run` seeds the built-in templates once.

## The `/p/{id}/` proxy

Only `/api/*` under a project is proxied. Every other path is served from the
embedded bundle under that prefix, running or not, so a stopped project still
renders the product UI with its start button. The target is re-resolved per
request from `p.Ports.WebUI`, so a restart on another port is picked up at once.
The prefix is stripped, the query preserved, `X-Forwarded-*` set, and SSE stays
unbuffered. An unknown id is a 404; no assigned port is a 502.

## Basic auth and where the credential lives

Auth is opt-in and gates only non-loopback peers: `127.0.0.0/8` and `::1`
always pass, so a local browser never sees a prompt. `StartupAuth` persists an
`Options.Auth` spec (`user:pass`, or a bare password with user `niq`) or loads
it from `store.AuthConfigPath()` (`<niq root>/common/auth.json`, `0600`). A
non-loopback bind with no credentials starts anyway and logs a warning naming
the file; `Run` repeats it on stderr. Comparison is constant-time.

## Binding, and `PortInUseError`

`Bind` returns the resolved host:port (so `:0` reports the OS choice) and
`OnReady` hands it back before serving. A taken address becomes
`*PortInUseError`; `IsPortInUse` also accepts a raw `EADDRINUSE`. The gateway
only *detects* it — stopping the holder or picking another port is a terminal
decision, so it lives in `cli`. Shutdown on SIGINT/SIGTERM allows 5s.

## What it does not do

It does not run a project in its own process (`launcher.Start` execs), serve a
project's data API (`project/api` does, on the project's own port), or know
which UI is mounted or whether the caller is a browser.

## Files

| File | Responsibility |
|---|---|
| `gateway.go` | `Options`/`Run`, the `Gateway` shell, `New`, `SetBasicAuth`, the route table and CORS wrapper, `PortInUseError`/`IsPortInUse`, `Bind`, `ResolvedAddr`, `Start` and shutdown. |
| `context.go` | `ContextInfo`, the mode marker the SPA reads; only `control` is encoded here. |
| `auth.go` | `AuthConfig` persistence, `StartupAuth` and its warning, `ParseAuthSpec`, `BasicAuth`, the loopback bypass, constant-time comparison. |
| `proxy.go` | The `/p/` prefix, the per-request `proxyTarget`, prefix strip, X-Forwarded, SSE flush, and the bundle-before-proxy rule with its 404/502 answers. |
| `projects.go` | Project handlers: list, server-side directory picker, link, create-and-start, start, stop, restart. |
| `providers.go` | `GET`/`PUT /api/providers`: verbatim read (or `provider.Example` when absent), name validation, whole-file replace. |
| `templates.go` | Template CRUD, zip import with a size cap, project→template preview, and the `writeTemplate` helper. |
| `ui.go` | `UIMount`, `Mount`, longest-prefix `mountFor`, `serveFrom`'s `index.html` fallback, and `rootUI` for the stopped-project shell. |
| `gateway_test.go` | In-process plane on an ephemeral port: context, projects, templates, link, dirs, start/stop errors, import, providers. |
| `proxy_test.go` | Unknown id 404, not-running shell plus 502, stopped shell with assets, prefix/query/forwarded headers, unbuffered SSE. |
| `auth_test.go` | `StartupAuth` persist/reload/`0600`/warning/invalid-spec cases, the loopback gate, the `ParseAuthSpec` table. |
| `port_test.go` | `Bind` produces a typed `PortInUseError`; `IsPortInUse` ignores permission and bad-address errors. |

## Invariants

- One surface, one policy: every UI reaches niq through this port, so auth,
  proxying and mounting each exist once.
- The gateway starts projects but never hosts them; an instance is tracked by
  its persisted ports, not by process parentage.
- Non-`/api/` paths under `/p/{id}/` are never proxied; they come from the bundle.
- A proxied request re-reads the upstream port each time.
- Loopback is always unauthenticated; credentials gate only remote peers.
- No non-test file imports a UI bundle or `project/api`.
