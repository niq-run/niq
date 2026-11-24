# contract — the project API's wire shapes and its client

`contract` is the boundary between a niq gateway and anything that talks to
it: the JSON that `/api/projects` returns and the small HTTP client that
decodes it. `View` is one project as the listing shows it, `Instance` is the
reply to a start or restart, and `Ports` is the address pair both carry.

It is deliberately **not** the disk shape. `project.json` is `store.Project`;
`launcher.ViewOf` is the only conversion to `View`, and the gateway encodes
what that built. A field added to the file stays private until it is added to
`View` as well. That is also why this package imports nothing but the standard
library: any surface, including one on another machine, may import it without
inheriting the single-machine form.

## Where it sits

```
project.json (store.Project)
      │ launcher.ViewOf
      ▼
contract.View / Instance ──encoded──► gateway /api/projects
      ▲                                     │
      └──────── contract.Client ◄───────────┘
                   (tui picker, niq status)
```

## The three shapes

| shape | carries | produced by |
|---|---|---|
| `View` | id, `created_at`, ports, the directory resolved on the server side, and `running` probed now | `launcher.ViewOf` |
| `Instance` | the WebUI and bus ports a start or restart actually bound, plus `already` | `launcher.Start` / `Restart` |
| `Ports` | the bus and WebUI ports, zero meaning "not allocated" | shared: `store.Project` embeds it rather than redeclaring it |

`Dir` and `running` exist so a client never has to know the directory layout
or how an instance is probed; both are decided on the gateway side.

## Files

| File | Responsibility |
|---|---|
| `shape.go` | The three wire types and nothing else: `Ports`, `View`, `Instance`, with the JSON tags clients decode. |
| `client.go` | `Client`, `New`, `ProjectURL`, the four verbs a UI needs (`List`, `Start`, `Restart`, `Stop`), and the shared request/response handling: ids escaped, a non-2xx reply surfaced as the gateway's own message rather than a silent success. |
| `client_test.go` | An `httptest` gateway stub pinning the method and path of every verb, id escaping, the `ProjectURL` shapes, the decoded `View`/`Instance`, and error passthrough (body text, status fallback, decode failure). |

## Invariants

- **Standard library only.** Nothing from `github.com/niq-run/niq` is
  imported, so any surface can depend on it without inheriting this form.
- **One-way conversion.** `View` is built by `launcher.ViewOf`; nothing here
  reads `project.json` or imports `store`.
- **The shell path comes from `ProjectURL`.** It always yields `/p/<id>/`, the
  gateway's mount, never a project's own data port.
- **Ids stay one path segment.** Every id is path-escaped, so `a/b` cannot
  widen the route.
- **Server messages survive.** Any non-2xx reply becomes an error carrying the
  gateway's body verbatim (trimmed), falling back to the status line when the
  body is empty; a 204 or empty success is not a decode failure.
- **A client carries no session.** `New` trims a trailing slash and installs a
  15-second timeout; a zero `Client` has no HTTP client and no trimmed base
  URL, so `New` is the constructor to use.

The stdlib-only rule is checkable:

```sh
go list -f '{{join .Imports "\n"}}' ./app/standalone/contract | grep 'github.com/niq-run/niq'  # empty
```
