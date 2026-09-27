# niq

niq is an **event-driven agent runtime**. It is a single agent composed of many **Workers** that collaborate over an event bus — each worker handles its own focus, and together they form the complete agent.

niq has exactly three core concepts:

- **Worker** — a unit that can do things, like an **actor**: it has its own state and communicates only by sending and reacting to messages. A Worker subscribes to events, processes them, and publishes new events. The Worker is niq's only extension point.
- **Program** — the source code of a Worker's capability: a natural-language prompt (like the skills of mainstream agents) or a DSL script (like programmatic tool calling, PTC). Both forms are source code for generating tool calls.
- **Event** — the only communication language between Workers, delivered over the event bus.

> **Collaboration is extension.**

niq grows not by adding new abstractions, but by adding more participants that cooperate over the event bus. Every capability (reasoning, tool execution, safety guards, lifecycle) is a Worker. Extending a capability and having a human join the collaboration are the same act — a user is just another collaborating participant, and there can be one or many of them. In niq, everything is workers collaborating over the event bus.

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Install

**npm** (recommended):

```sh
npm install -g @niq.run/niq
```

**Install script** (installs to `~/.niq/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/install.sh | sh
```

**From source** (Go 1.25+):

```sh
git clone https://github.com/niq-run/niq
cd niq && make build   # → ./bin/niq
```

## Quick start

niq has two layers: a **control plane**, and **projects** — each project is an isolated agent instance with its own event bus, worker set and WebUI on its own ports.

```sh
# start the control plane (default :9527)
niq
```

Open **http://localhost:9527** — the control plane's WebUI lists your projects and can create and start one from a template, no terminal needed.

Prefer the CLI? Both steps can also be done manually:

```sh
niq project create my-project
niq project run my-project
```

Each project serves its own WebUI on a loopback-only port, so it is never
exposed by accident: open it through the control plane at
**http://localhost:9527/p/&lt;id&gt;/**, which proxies to that project. Ports are
assigned dynamically on first run and then persisted per project, so they stay
stable across restarts (`--bus` / `--webui` override). `niq project list` shows
each project's `webui` and `bus` ports.

### Server deployment

The control plane is the only port a deployment needs to expose: it serves the
SPA, the project-management API and every project's WebUI (at `/p/<id>/`).
Projects and the event bus bind loopback only, so they stay unreachable from
outside even on a public machine.

```sh
niq --addr :9527 --auth alice:s3cret
```

Configure credentials before exposing the port: requests from any non-loopback
address are then answered with a 401 until they authenticate, while requests from
the machine itself stay password-free. `--auth` (control plane) and
`--webui-auth` (a project started by hand) persist to and read from the same
`~/.niq/common/auth.json`, so one pair of credentials covers everything. Without
credentials remote callers are locked out entirely, and niq warns about the
binding at startup.

Basic auth sends the credentials on every request, so terminate TLS in front (or
tunnel through SSH/WireGuard) before exposing niq to the open internet — niq
itself speaks plain HTTP.

After upgrading the binary, restart the running projects: their page and static
assets are served by the control plane, and an old project process may reference
asset files the new binary no longer contains.

### Model provider

The `reason` worker talks to an LLM provider. Provider settings (endpoint, model, API key) live in `~/.niq/common/providers/provider.json`; `api_key` and header values support environment-variable expansion — e.g. `"api_key": "${OPENAI_API_KEY}"` keeps secrets out of stored files.

## Project layout

```
core/itfs/ interfaces & types (contracts, not implementations)
core/impl/ implementations (workers, bus, transports, providers, hosts)
app/       control plane, project runtime, WebUI
cmd/       CLI entry point
npm/       npm distribution (launcher shim + package template)
ext/       external worker implementations (HTTP)
```

## Build & test

```sh
go build ./... && go vet ./...
go test ./core/impl/eventbus/ -count=1
```

The WebUI bundle is committed (it is `go:embed`-ed), so rebuild and commit it
after changing anything under `app/webui/assets/src`:

```sh
cd app/webui/assets && npm run build
```

### Frontend development

With the control plane running, the Vite dev server (`npm run dev` in
`app/webui/assets`, port 5173) proxies API traffic to :9527, so both pages
work with HMR:

- control plane UI: http://localhost:5173/
- a project's UI: http://localhost:5173/p/&lt;id&gt;/ (that project must be running)

## Data directory

Runtime data lives under `~/.niq/` by default. Set the `NIQ_HOME` environment
variable to relocate this root to a custom directory (for a portable install,
tests, or to keep state out of your home dir); `~`/`~/` in the value are
expanded. `NIQ_AUTH_CONFIG` and `NIQ_PROVIDER_CONFIG` still override those two
individual files wholesale, taking priority over `NIQ_HOME`.

```
~/.niq/
  bin/                         prebuilt binary (install.sh)
  niq.log                      control-plane log
  common/
    templates/                 project templates
    providers/provider.json    shared provider config (API keys via env expansion)
  projects/<id>/               one directory per project
    id/identities.json         worker identity registry (owned by the bus)
    programs/                  Program storage
    logs/                      daily-rotated run logs
```
