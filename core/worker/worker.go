package worker

import (
	"context"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// Worker is the contract for built-in Go workers running inside
// WorkerService. External workers connect to the bus via UDS/WS
// and are not constrained by this interface.
type Worker interface {
	// ID returns the unique identifier of this Worker.
	ID() string

	// Start enters the Worker's idle loop, waiting for events.
	Start(ctx context.Context) error

	// Subscriptions returns the event patterns this Worker subscribes to.
	Subscriptions() []event.EventPattern
}

// ManagedWorker extends Worker with Snapshot / Restore to support
// suspend-resume and crash recovery. Implementations serialize their
// full execution state and rehydrate it from a blob.
type ManagedWorker interface {
	Worker

	// Stop terminates the Worker and releases resources.
	Stop() error

	// Snapshot captures the worker's current execution state as an opaque blob.
	// The blob may be persisted and later passed to Restore to resume the
	// worker from the same state — enabling suspend / resume and recovery.
	Snapshot() ([]byte, error)

	// Restore rehydrates the worker from a blob previously returned by
	// Snapshot. Called after construction and before Start.
	Restore(state []byte) error
}

// WorkerState is the lifecycle state of a managed worker.
type WorkerState string

const (
	// StateRunning means the worker is started and listening on the bus.
	StateRunning WorkerState = "running"
	// StateSuspended means the worker's goroutine is stopped, its bus
	// connection is released, and it is not listening. It can be resumed
	// manually (or, later, by a configured wake event).
	StateSuspended WorkerState = "suspended"
)

// WorkerConfig is the serializable definition of a worker — the inputs
// needed to (re)build it. It is persisted so a worker can be resumed after
// a process restart. Unlike SpawnSpec, it contains no closures.
type WorkerConfig struct {
	// ID is the worker's unique identifier.
	ID string `json:"id"`
	// Type is the worker type label (reason, workspace, ...).
	Type string `json:"type"`
	// Params holds type-specific construction parameters.
	Params map[string]any `json:"params,omitempty"`
}

// SpawnSpec describes how to construct and connect a managed worker.
// It decouples the worker instance from its bus connection so a worker can
// be suspended (stop + disconnect) and later resumed (reconnect + start).
//
// Connect/Build are closures provided by the assembly layer (swarm); they are
// not persisted. Only Config is serialized — on restart the assembly re-
// materializes Connect/Build from Config.
type SpawnSpec struct {
	// Config is the single source of truth for the worker's identity (ID,
	// Type) and construction params. It is persisted for resumability.
	Config WorkerConfig

	// Connect creates and connects a fresh worker-side channel. Each call
	// must return a new, independent connection so a suspended worker can be
	// reconnected on resume.
	Connect func() (corebus.WorkerSideChannel, error)
	// Build constructs a worker instance bound to the given channel.
	Build func(ch corebus.WorkerSideChannel) ManagedWorker
}

// ID returns the worker's unique identifier.
func (s SpawnSpec) ID() string { return s.Config.ID }

// Type returns the worker type label.
func (s SpawnSpec) Type() string { return s.Config.Type }
