## Run & Debug

### API Key

Put your API key in `~/.zshenv`:

```
export OPENAI_API_KEY=sk-xxxx
```

(`.zshrc` only runs in interactive shells — use `.zshenv`.)

### Build & Test

```
cd niq && go build ./... && go vet ./...
cd niq && go test ./pkg/services/bus/ -count=1
```

### Run

```
cd niq && go run ./cmd/niq/
```

## Conventions

### Language

- Code comments and identifiers: **English**
- Design documents: **Chinese** (`docs/design/`)
- Dev notes: **Chinese** (`docs/dev_notes/`)

### Package discipline

- `core/` — interfaces and types a **worker** needs (contracts, not implementations)
- `pkg/` — all implementations (workers, services, bus, transports)
- Each `core/` sub-package should have a `README.md` explaining its contract

## Design Discipline

niq has exactly one extensibility concept: **Worker**.

There is no Agent, Plugin, Hook, Middleware, Controller, Supervisor,
or Admin API. If something new needs to be added to the system, the
first question is: can it be a Worker?

### Worker architecture

Every worker has the same shape — a bus-facing protocol layer and a
domain-specific backend:

```
Worker (bus protocol)  ──→  domain backend (pure service, no bus)
  workspace worker     ──→  wsbackend (filesystem)
  host worker          ──→  workerhost (lifecycle engine)
  hiw worker           ──→  TUI model (user interaction)
  reason worker         ──→  LLM provider
```

The domain backend never touches the bus — it's a plain Go service
with no import of `core/bus` or `core/event`.

### Control plane is in the data plane

Lifecycle management is a Worker like any other (HostWorker). The
control plane communicates through the same event bus as everything
else — there is no privileged channel. HostWorker's *Bus reference
(for RegisterWorker) is a deployment-time capability, not a bus-level
status.

### Trust model

| | Local domain | Remote |
|---|---|---|
| Scope | in-process + HTTP loopback | HTTPS |
| Data-plane auth | self-declared workerID | Token |
| Control-plane auth | none | OpenAPI key |

### Key decisions already made

- One Worker abstraction. No second-class extension points.
- In-process bus via InProcessClient, cross-process via HttpClient → HttpTransport.
- Bus enforces identity (anti-spoofing) and ACL (PublishAllow / SubscribeAllow).
- `PublishAllow` is exact match. `SubscribeAllow` supports wildcards.
