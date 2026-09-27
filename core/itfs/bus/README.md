# niq event bus — core protocol

The bus is the sole communication medium for all workers in a niq swarm. It defines the protocol by which workers discover each other, exchange events, and collaborate — without hard references, without RPC, without shared memory.

## Protocol layers

The bus protocol has three layers, each with a distinct lifecycle:

```
┌─────────────────────────────────────────────────────────┐
│  Identity (control plane, offline, persistent)          │
│  Who a worker is, what it may publish and subscribe to  │
├─────────────────────────────────────────────────────────┤
│  Channel (data plane, runtime, ephemeral)               │
│  A paired connection: bus's view + worker's view        │
├─────────────────────────────────────────────────────────┤
│  Subscription (data plane, persistent)                  │
│  Matching event types to interested subscribers         │
│  (internal to bus engine, no separate interface)        │
└─────────────────────────────────────────────────────────┘
```

## Identity

`Identity` is an offline registration record. It is created by the control plane and persists independently of any runtime connection. A worker that crashes and reconnects keeps its identity — the channel is new, but who the worker is and what it subscribes to remains the same.

An identity carries:
- `WorkerID` — unique address on the bus
- `Credential` — authentication token for connect-time verification
- `PublishAllow` — event types the worker may publish
- `SubscribeAllow` — event patterns the worker subscribes to

`IdentityRegistry` is the control-plane interface for managing identities: register, update allow lists, revoke, and look up.

## Channel

A channel is a paired connection between the bus and a worker. It has two sides:

### BusSideChannel (bus's view)

The bus pushes already-routed events to the worker via `Send`, and reads delivery requests from the worker via `Receive`.

```go
type BusSideChannel interface {
    Send(ctx, evt)                       // deliver a routed event
    Receive(ctx) → <-chan Request        // read delivery requests
    Close()
}
```

When the `Receive` channel is closed, the bus considers the worker disconnected and cleans up the link. The Identity is preserved.

### WorkerSideChannel (worker's view)

The worker sends events to specific targets or broadcasts them, and receives events routed to it by the bus.

```go
type WorkerSideChannel interface {
    Send(ctx, evt, targets...)           // directed delivery
    Broadcast(ctx, evt)                  // publish to all subscribers
    Receive(ctx) → <-chan Event          // receive routed events
    Close()
}
```

Behind the scenes, `Send` and `Broadcast` each construct a `Request` and send it through the transport. The worker does not touch `Request` directly — it is a protocol-level detail.

### Request (protocol message)

`Request` is what flows from the worker to the bus. It carries the event payload and routing intent.

```go
type Request struct {
    Type    RequestType    // send or broadcast
    Events  []event.Event
    Targets []string       // for send requests
    TraceID string
}
```

Connection liveness (heartbeat, disconnect) is handled by the transport layer, not by the request protocol. The bus detects disconnection when the `Receive` channel of a `BusSideChannel` is closed.

## Subscription matching

Subscription matching is an internal concern of the bus engine, not a separate interface. When a worker broadcasts an event, the bus engine looks up which registered identities have matching `SubscribeAllow` patterns and delivers to those that are currently online.

Subscriptions are populated from `Identity.SubscribeAllow` when an identity is registered. They persist across connect/disconnect cycles — a worker does not need to resubscribe after reconnecting.

## Putting it together

```
Worker A                  Bus                    Worker B
    │                      │                        │
    │── Send(evt, B) ─────→│                        │
    │   (RequestSend)      │── Send(evt) ──────────→│
    │                      │                        │
    │── Broadcast(evt) ───→│                        │
    │   (RequestBroadcast) │── (if B subscribes) ───→│
    │                      │                        │
    │←── Receive(evt) ─────│── Send(evt) ──────────│
    │                      │   (from Worker B)      │
```

## Transport independence

These interfaces are transport-agnostic. The same protocol works for in-process channels, HTTP/SSE, Unix sockets, or remote relays. A transport implementation provides both sides of the channel pair:

```
transport = BusSideChannel implementation + WorkerSideChannel implementation
```

The bus core never needs to know whether a worker is local or remote. It only sees the channel interface.