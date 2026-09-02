# niq

> **niq runs programs that haven't been written yet.**

niq is an **event-driven, decentralized agent runtime**. It is a single agent, but one composed of many **Workers** (a Worker Swarm) that collaborate over an event bus — each worker handles its own focus, and together they form the complete agent.

niq has exactly three core concepts:

- **Worker** — a unit that can do things, like an **actor**: it has its own state and communicates only by sending and reacting to messages. A Worker subscribes to events, processes them, and publishes new events. Every capability (reasoning, tool execution, safety guards, lifecycle) is a Worker; there is only one extension concept.
- **Program** — the source code of a Worker's capability (natural-language Prompts, or formalized DSL Scripts).
- **Event** — the only communication language between Workers, delivered over the event bus.

Core insight: **collaboration is extension.** niq grows not by adding new abstractions, but by adding more participants that cooperate over the event bus. Extending a capability and having a human join the collaboration are the same act — a user is just another collaborating participant, and there can be one or many of them. In niq, everything is workers collaborating over the event bus.

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Install

**npm** (recommended) — the binary for your platform is installed automatically as an npm optional dependency, no postinstall scripts:

```sh
npm install -g @niq.run/niq
```

**Install script** — downloads the prebuilt binary from GitHub Releases into `~/.niq/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/install.sh | sh
```

**From source** — Go 1.25+:

```sh
git clone https://github.com/niq-run/niq
cd niq && make build   # → ./bin/niq
```

## Quick start

niq has two layers: a **control plane**, and **projects** — each project is an isolated agent instance with its own event bus, worker set and WebUI on its own ports.

```sh
# 1. start the control plane (default :9527)
niq

# 2. create a project from the default template
niq project create my-project

# 3. run it in this process
niq project run my-project
```

Ports are assigned dynamically on first run and then persisted per project, so they stay stable across restarts (`--bus` / `--webui` override). `niq project list` shows each project's `webui` and `bus` ports — open the WebUI in your browser to chat, watch the event flow and manage workers.

### Model provider

The `reason` worker talks to an LLM provider. Provider settings (endpoint, model, API key) live in `~/.niq/common/providers/provider.json`; `api_key` and header values support environment-variable expansion — e.g. `"api_key": "${OPENAI_API_KEY}"` keeps secrets out of stored files.

## Project layout

```
core/      interfaces & types (contracts, not implementations)
pkg/       implementations (workers, services, bus, transports)
internal/  control plane, project runtime, WebUI
cmd/       CLI entry point
npm/       npm distribution (launcher shim + package template)
ext/       external worker implementations (HTTP)
```

## Build & test

```sh
go build ./... && go vet ./...
go test ./pkg/eventbus/ -count=1
```

## Data directory

Runtime data lives under `~/.niq/`:

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
