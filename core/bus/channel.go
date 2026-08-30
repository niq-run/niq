package bus

import (
	"context"

	"github.com/niq-run/niq/core/event"
)

// BusSideChannel is the bus's view of a connection to a worker.
//
// The bus uses Send to deliver already-routed events to the worker,
// and Receive to read delivery requests from the worker.
//
// A BusSideChannel is created when a worker connects and is destroyed
// when the worker disconnects. The worker's Identity persists across
// disconnections — only the channel is ephemeral.
//
// When the Receive channel is closed, the bus considers the worker
// disconnected and cleans up the link.
type BusSideChannel interface {
	// ID returns the transport-level connection identifier.
	// This is not the worker ID — it is a connection handle assigned
	// by the transport layer (e.g., a session ID).
	ID() string

	// WorkerID returns the identity of the worker on the other end.
	WorkerID() string

	// Send delivers a fully-routed event to the worker.
	// The event has already been through routing — the worker
	// only needs to consume it.
	Send(ctx context.Context, evt event.Event) error

	// Receive returns a channel of delivery requests from the worker.
	// When the channel is closed, the worker has disconnected.
	Receive(ctx context.Context) (<-chan Request, error)

	// Close closes the connection and releases resources.
	// After Close, the bus should not call Send or Receive.
	Close() error
}

// WorkerSideChannel is the worker's view of a connection to the bus.
//
// A worker uses Send to deliver events to specific target workers,
// Broadcast to publish events to all matching subscribers, and
// Receive to read events routed to it by the bus.
//
// Behind the scenes, Send and Broadcast each construct a Request
// and send it through the transport. The worker does not need to
// know about Request — it is a protocol-level detail.
//
// The worker does not need to know whether the bus is in-process,
// over HTTP, or behind a relay — the interface is the same.
type WorkerSideChannel interface {
	// ID returns the transport-level connection identifier.
	ID() string

	// Send delivers a directed event to specific target workers.
	// The event is routed directly to the specified workers without
	// subscription matching. targets must be non-empty — use
	// Broadcast for undirected delivery.
	Send(ctx context.Context, evt event.Event, targets ...string) error

	// Broadcast delivers an event to all workers whose SubscribeAllow
	// matches the event's type. The bus performs subscription matching
	// and delivers to all online subscribers. If no worker is
	// subscribed, the event is silently dropped — this is a valid
	// outcome, not an error.
	Broadcast(ctx context.Context, evt event.Event) error

	// Receive returns a channel of events routed to this worker.
	// Events may come from Send (directed) or Broadcast (matched
	// by subscription). The worker does not need to distinguish
	// between the two sources.
	Receive(ctx context.Context) (<-chan event.Event, error)

	// Close closes the connection and releases resources.
	Close() error
}

// Request is a delivery request sent from a worker to the bus.
//
// Workers do not call bus methods directly — they send requests
// through their side of the channel. The bus reads these requests
// and executes them: matching subscribers, routing events.
type Request struct {
	// Type indicates the kind of delivery request.
	Type RequestType

	// Events carries the event payload for Send and Broadcast requests.
	Events []event.Event

	// Targets specifies the recipient workers for Send requests.
	// This field is ignored for Broadcast requests.
	Targets []string

	// TraceID carries the distributed tracing context.
	TraceID string
}

// RequestType classifies a Request.
type RequestType string

const (
	// RequestSend carries a directed event to specific target workers.
	RequestSend RequestType = "send"

	// RequestBroadcast carries an event to all matching subscribers.
	RequestBroadcast RequestType = "broadcast"
)