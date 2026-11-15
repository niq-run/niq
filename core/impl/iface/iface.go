// Package iface provides the interface worker: a worker whose job is not a
// domain of its own but a seam between the bus and an external world.
//
// It faces the bus like every worker (identity, ACL, presence, discover, tools)
// and behind it holds one or more Backends — the thing that implements its
// capability. That is the same shape every other worker has (a workspace worker
// holds a filesystem backend, a reason worker holds an LLM); what makes this one
// "interface" is only that its capability is reaching outward: a user interface,
// an API application, a chat-platform bridge, another niq instance.
//
// The division of labour is deliberately narrow:
//
//   - the worker owns the bus: it receives what is addressed to it, serves its
//     tools, announces presence, and provides the four event operations a
//     backend needs — Send, Broadcast, Request (send + await the correlated
//     reply), and OnEvent (interest in inbound events);
//   - a backend owns everything outward: how to render, how to reach a remote
//     system, what its own durable state is.
//
// It is NOT a bus control plane. Editing a worker's ACL, rendering the global
// event stream, or injecting an event into that stream are the layers above
// (the app, its bus handles); routing them through an interface worker would add
// a hop and hide what they really are.
package iface

import (
	"context"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// DefaultRequestTimeout is how long Request waits for a reply when the caller
// does not set one. It matches the WebUI's long-standing ask timeout: lifecycle
// and LLM-backed tools can be slow, but a request with no answer by then is a
// failure the caller has to see.
const DefaultRequestTimeout = 30 * time.Second

// Handler is what a Backend may do on the bus, as this worker's identity. It is
// the bus's own verbs, nothing more: there is no "call any tool" or "manage
// workers" here, because the identity's ACL already bounds what it may publish.
type Handler interface {
	// ID is the worker id the handler speaks as.
	ID() string

	// Send delivers evt to the named targets only.
	Send(ctx context.Context, evt event.Event, targets ...string) error

	// Broadcast publishes evt to every identity subscribing to its type.
	Broadcast(ctx context.Context, evt event.Event) error

	// Request sends evt to target and waits for the correlated reply. It returns
	// the request.completed event, or an error carrying the reply's message for
	// request.failed / request.rejected, a timeout, or the context's error.
	//
	// The correlation is not a new protocol: the reply is an ordinary event
	// addressed to this worker. Only the side holding the channel can pair it, so
	// this is the one place that owns the pairing (trace id, request id, timeout).
	Request(ctx context.Context, target string, evt event.Event) (event.Event, error)

	// OnEvent registers interest in inbound events. No types means every event
	// delivered to this worker; otherwise only the listed types (exact match,
	// plus "*" for all). Several backends may match the same event — each gets its
	// own copy, in delivery order, on its own goroutine.
	OnEvent(fn func(event.Event), types ...event.EventType) Subscription
}

// Subscription cancels an OnEvent registration. It is safe to call more than
// once, and safe to call after the worker stopped.
type Subscription interface {
	Close()
}

// Backend is one outward-facing surface of an interface worker: the thing behind
// it that implements the capability. It is injected by the assembly layer,
// because it is a process-local object (an HTTP server, a terminal UI, a
// connection) rather than something a declaration's params could describe.
//
// SetHandler is called exactly once, before the worker starts and before any
// event is delivered, so a backend can build its outward side knowing how to
// reach inward.
type Backend interface {
	SetHandler(h Handler)
}

// Stateful is the optional durable state of an interface worker, owned by its
// backend (e.g. a bridge's recipient bindings). Supplied through
// Config.State because only the builder knows which backend holds it.
type Stateful interface {
	Snapshot() ([]byte, error)
	Restore(state []byte) error
}
