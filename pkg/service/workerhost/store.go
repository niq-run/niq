package workerhost

import (
	"github.com/niq-run/niq/core/worker"
)

// WorkerRecord is the persisted representation of a managed worker: its
// definition and its lifecycle state (with the latest snapshot).
type WorkerRecord struct {
	ID       string
	Type     string
	Params   map[string]any
	State    worker.WorkerState
	Snapshot []byte
}

// WorkerStore is the persistence backend for managed workers. WorkerService
// calls Save* on every lifecycle transition and LoadAll on recovery. The
// concrete implementation lives in the assembly layer (swarm), which owns the
// storage layout — workerhost only depends on this contract.
type WorkerStore interface {
	// SaveConfig writes the worker's serializable definition.
	SaveConfig(cfg worker.WorkerConfig) error
	// SaveState writes the worker's lifecycle state and latest snapshot.
	SaveState(id string, state worker.WorkerState, snapshot []byte) error
	// LoadAll returns every persisted worker record.
	LoadAll() ([]WorkerRecord, error)
	// Delete removes a worker's persisted directory.
	Delete(id string) error
}
