# mcp — built-in remote MCP proxy worker

`mcp` is a built-in Go worker that connects to remote **MCP** servers (Streamable
HTTP, via the official `modelcontextprotocol/go-sdk`) and proxies their tools
onto the niq bus. It is the HTTP counterpart of the host's ephemeral stdio MCP
processes: here the servers are already deployed elsewhere, and niq acts as the
client.

A remote tool is invoked through the same request/response convention as any
other capability: the caller names the tool, the worker forwards the call to the
remote session, and the result comes back as `request.completed` /
`request.failed` under the caller's echoed `RequestId`.

## Event surface

| Event | Purpose |
|---|---|
| `mcp.server.list` | List the effective servers (name, url, protocol, status, cached tool count). |
| `mcp.tool.list` | List one server's tools (`{ server }`): name, description, input schema. |
| `mcp.tool.call` | Call a remote tool (`{ server, tool, arguments }`). |
| `mcp.server.add` | Add a server at runtime, prime its tools (connect + list), persist (`{ name, url, headers?, protocol? }`). |
| `mcp.server.remove` | Remove a server, disconnect, withdraw its tools, persist (`{ name }`). |
| `mcp.server.update` | Change a server's url/headers/protocol, drop the old session, re-list (async), persist (`{ name, ... }`). |
| `mcp.expose` | Read (`{}`) or switch (`{ mode }`) the exposure mode. |
| `worker.readme` | Return this worker's usage guide plus a snapshot of the current mode and servers (the agent-facing "skill card"). |

`mcp.server.add/remove/update` and `mcp.expose` are **only registered in dynamic
mode** (see below); `worker.readme`, `mcp.server.list`, `mcp.tool.list` and
`mcp.tool.call` are always registered.

## Exposure modes

`expose` selects the discovery surface:

- **`agentic` (default)** — one small, fixed surface: `mcp.server.list` →
  `mcp.tool.list(server)` → `mcp.tool.call(server, tool, args)`. The LLM
  namespace stays constant regardless of how many tools the remotes expose;
  tools are discovered on demand.
- **`ready`** — every cached remote tool is additionally registered as its own
  extension, `mcp.<server>.<tool>`, so peers discover them flat in
  `worker.ready`. At start the worker primes all servers' tool lists off the
  start path and re-announces; add/update/tool.list refresh and re-announce.

`mcp.expose { mode }` switches at runtime. Switching to `agentic` withdraws every
per-tool extension (the cached lists are kept) and re-announces; switching to
`ready` re-primes and re-announces. The mode rides the snapshot, so it survives a
restart.

## Fixed mode (config-only tool provider)

`fixed: true` turns the worker into a static tool provider:

- servers and expose come from config only;
- `mcp.server.add/remove/update` and `mcp.expose` are not registered — no runtime
  mutation, no mode switching;
- no runtime state is persisted (`Snapshot` returns nil, `Restore` is a no-op);
  the config is authoritative across restarts.

Typical fixed setup: `fixed: true` + `expose: "ready"` — connect at start,
flatten the configured servers' tools into the ready contract, done.

## Configuration

Config lives in the worker's `params` map (flat, like the other workers):

```jsonc
{
  "type": "mcp",
  "id": "mcp",
  "params": {
    "fixed": false,          // optional; true = config-only fixed tool provider
    "expose": "agentic",     // agentic (default) | ready
    "servers": [
      {
        "name": "github",
        "url": "https://api.githubcopilot.com/mcp",
        "protocol": "streamable-http",   // informative; the client speaks Streamable HTTP
        "headers": { "Authorization": "Bearer ${GITHUB_TOKEN}" }
      }
    ]
  }
}
```

- `servers[].headers` values may reference `${VAR}`; they are resolved against
  the process environment (project env is injected into the niq process).
- The legacy nested `params.mcp = { expose, fixed, servers }` form is still
  accepted; flat keys win on a conflict.
- `protocol` is carried through and shown by `mcp.server.list`, but the client
  always speaks **Streamable HTTP** today.

## Persistence

Dynamic mode persists the **server set and expose mode** through the generic
`ManagedWorker` snapshot contract:

- `Snapshot()` returns `{"servers":[...],"expose":"..."}` — always a blob, even
  for an empty set, so removing every server survives a restart instead of
  falling back to the static baseline;
- `Restore()` applies the snapshot (validated; an unknown/absent expose keeps
  the constructor config) and unregisters any tool extensions bound by the
  replaced instance;
- add/remove/update/expose call `NotifyDurableChange()`, which the host turns
  into a `Checkpoint` (`Snapshot` + save) off the event loop.

Cached tool lists are **not** persisted: they are derivative, re-fetched at
start (`ready`) or on demand (`agentic`).

## Discovery and dispatch

- Capabilities are extensions: `WatchEntries` renders the registry into
  `worker.ready`, and `process` routes incoming events through
  `DispatchExtension`.
- In `ready` mode, `setTools` binds one extension per tool
  (`mcp.<server>.<tool>`) with the tool's description/schema, tracked per server
  in `toolEvents`; `setTools(server, nil)` withdraws exactly that server's
  extensions. Withdrawal needs no registry surgery beyond
  `BaseWorker.Unregister`.
- Reserved events: a remote tool whose sanitized event name would collide with a
  built-in one (`mcp.server.add`, `mcp.tool.call`, `mcp.expose`, …) is skipped and
  logged, so it can never clobber a management or discovery extension.
- Broadcast delivery needs subscriptions (`worker.discover`, `worker.ready`,
  `worker.gone` by default); tool invocations are directed and reach the worker
  regardless.

## Remote client

- One `serverClient` per server, created from config; the session is established
  lazily on the first list/call (`EnsureConnected`).
- The transport is the official SDK's Streamable HTTP client; `protocol` is
  informational for now.
- A failed list/call drops the session; the next call reconnects.
- `ListTools` caches `toolInfo` (name, description, input schema) per server.
- `CallTool` renders the result as text (`TextContent`, falling back to
  marshaled structured content).

## Files

| File | Responsibility |
|---|---|
| `worker.go` | the worker: config, lifecycle (`Start`/`Stop`/`Snapshot`/`Restore`), the event loop, discovery/management/expose handlers, the tool cache and its extension bindings |
| `config.go` | `ServerConfig` / `Config`, `ParseConfig` (flat keys + legacy nested form), expose modes, `${ENV}` header resolution |
| `client.go` | the `serverClient` wrapper over the official go-sdk: Streamable HTTP transport, `ListTools` / `CallTool`, result rendering |
| `worker_test.go` | lifecycle, snapshot/restore, expose switching, ready-mode registration/withdrawal, fixed mode |
| `config_test.go` | flat/legacy parsing, defaults, validation |
| `client_test.go` | the SDK client wrapper against a local test server |

## Notes

- **Runtime add is privileged**: it lets a worker connect to an arbitrary URL and
  turn that server's tools into reason-callable capabilities. Scope it with bus
  subscribe/publish grants (e.g. only reason may call `mcp.server.*`).
- **Credentials**: headers currently support `${ENV}` references; dynamically
  added servers may carry plaintext headers until a vault is designed.
- **Naming**: dynamic tool events are `mcp.<sanitized-server>.<sanitized-tool>`;
  sanitization is lossy, so distinct names can theoretically collide (the last
  registration wins within a server).
