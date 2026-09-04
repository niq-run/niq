// Package bus defines the core protocol interfaces for the niq event bus.
//
// The bus protocol has three layers:
//
//   - Identity: offline registration of worker identity and capabilities
//   - Channel: runtime connection between a worker and the bus
//   - Subscription: matching event types to interested subscribers
//
// These interfaces are transport-agnostic. Implementations may use
// in-process channels, HTTP/SSE, Unix sockets, or any other transport.
package bus

import (
	"github.com/niq-run/niq/core/event"
)

// Identity represents a worker's registered identity on the bus.
//
// Identity is created offline by the control plane and persists
// independently of any runtime connection. When a worker disconnects
// and reconnects, its identity remains unchanged.
//
// The SubscribeAllow list determines which events the bus delivers
// to this worker via Broadcast. The PublishAllow list determines
// which event types this worker may publish via Send or Broadcast.
type Identity struct {
	// WorkerID is the unique identifier for this worker.
	WorkerID string

	// Type is the worker type label (e.g. "reason", "workspace", "hiw", "timer").
	// Populated at registration time by the project assembly.
	Type string

	// Credential is used to authenticate the worker at connect time.
	// The credential scheme is transport-specific (e.g., token, API key,
	// peer credential for loopback).
	Credential string

	// PublishAllow lists what this worker may publish: event types, each
	// optionally restricted to a specific target worker for directed sends.
	// Supports "*" (all), "Prefix.*" (prefix), and exact match on type.
	PublishAllow []event.PublishPattern

	// SubscribeAllow lists the event patterns this worker subscribes to.
	// Each pattern may restrict by type ("*", "Prefix.*", exact) and by
	// optional source worker.
	SubscribeAllow []event.EventPattern
}

// IdentityRegistry is the control-plane interface for managing identities.
//
// Implementations may store identities in memory, a database, or
// delegate to an external service.
type IdentityRegistry interface {
	// Register creates a new identity. Returns an error if the
	// worker ID is already registered.
	Register(id Identity) error

	// Update replaces the allow lists for an existing identity.
	// Returns an error if the identity is not found.
	Update(workerID string, pubAllow []event.PublishPattern, subAllow []event.EventPattern) error

	// Revoke removes an identity. Returns an error if not found.
	// Future connection attempts with this worker ID will fail
	// unless the identity is re-registered.
	Revoke(workerID string) error

	// Lookup returns the identity for a worker ID, or false if
	// not found.
	Lookup(workerID string) (Identity, bool)

	// List returns all registered identities.
	List() []Identity
}
