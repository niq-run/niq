# HIW — Human Interface Worker

HIW is the human's presence on the niq event bus. It translates human actions into bus events, and bus events back into a human-readable form.

## Responsibilities

- **SendInput** — publish human input as `worker.input` events to the bus
- **Follow / LoadBefore** — query + live-subscribe event streams, with Worker / TraceID / Type filtering
- **Workers** — track the set of active workers on the bus
- **PendingDecisions / MakeDecision** — manage `decision.requested` lifecycle, forward or resolve decisions

## Architecture

```
UI layer (TUI / WebUI)
    │  call hiw methods
    ▼
HIW Worker
    │  wraps bus operations
    ▼
Event Bus
```

UI layers never touch `bus` or `store` directly. They hold a `*hiw.Worker` and interact through `SendInput`, `Follow`, `PendingDecisions`, etc.

## File layout

| File | Contents |
|------|----------|
| `worker.go` | Worker struct, Config, New, Start, Stop, lifecycle |
| `events.go` | Event loop: handleEvent, handleLifecycle, getStr/matchesFilter utilities |
| `decision.go` | Decision types and handleDecision |
| `interface.go` | Public UI-facing methods + Filter type |

## Decision protocol

Any worker can request a decision by publishing `decision.requested.{id}`. Subscribers (HIW, Reason Workers, etc.) independently respond with `decision.made.{id}`.

HIW tracks pending decision requests and allows the user to respond via `MakeDecision`.
