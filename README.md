# niq

> **niq runs programs that haven't been written yet.**

niq is an **event-driven, decentralized agent runtime**. It is a single agent, but one composed of many **Workers** (a Worker Swarm) that collaborate over an event bus — each worker handles its own focus, and together they form the complete agent.

niq has exactly three core concepts:

- **Worker** — a unit that can do things, like an **actor**: it has its own state and communicates only by sending and reacting to messages. A Worker subscribes to events, processes them, and publishes new events. Every capability (reasoning, tool execution, safety guards, lifecycle) is a Worker; there is only one extension concept.
- **Program** — the source code of a Worker's capability (natural-language Prompts, or formalized DSL Scripts).
- **Event** — the only communication language between Workers, delivered over the event bus.

Core insight: **collaboration is extension.** niq grows not by adding new abstractions, but by adding more participants that cooperate over the event bus. Extending a capability and having a human join the collaboration are the same act — a user is just another collaborating participant, and there can be one or many of them. In niq, everything is workers collaborating over the event bus.

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Project layout

```
core/         interfaces & types (contracts, not implementations)
pkg/          implementations (workers, services, bus, transports)
internal/     control plane, project runtime, WebUI
cmd/          CLI entry point
doc/          design docs & dev notes
```

## Quick start

### Prerequisites

- Go 1.22+
- A model API key (for the reason worker), set in `~/.zshenv`:

  ```sh
  export OPENAI_API_KEY=sk-xxxx
  ```

### Run

```sh
# Start the control plane (listens on :9527 by default)
cd niq && go run ./cmd/niq/

# Or build first
make build
./bin/niq

# Then create and run a project from the WebUI, or from the CLI:
./bin/niq project create my-project
./bin/niq project run my-project
```

Once started, open <http://localhost:19763> for the WebUI — chat, inspect events, and view workers.

### Build & test

```sh
cd niq && go build ./... && go vet ./...
cd niq && go test ./pkg/service/eventbus/ -count=1
```

### Data directory

Runtime data lives under `~/.niq/`:

```
~/.niq/
  ├── id/identities.json    # worker identity registry (owned by the bus)
  ├── programs/             # Program storage
  └── niq.log               # run log
```
