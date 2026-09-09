package workerhost

import (
	"github.com/niq-run/niq/core/worker"
)

// StateRecord is the persisted runtime half of a worker: its lifecycle state
// and latest snapshot. The definition half (id/type/params) is not stored
// here — it belongs to whatever layer declares the worker (a project's
// project.json), which hands the config back on recovery.
type StateRecord struct {
	ID       string
	State    worker.WorkerState
	Snapshot []byte
}

// WorkerStore is the persistence backend for managed workers' runtime state.
// WorkerService calls SaveState on every lifecycle transition and LoadAll on
// recovery. The concrete implementation lives in the assembly layer (project),
// which owns the storage layout — workerhost only depends on this contract.
type WorkerStore interface {
	// SaveState writes the worker's lifecycle state and latest snapshot.
	SaveState(id string, state worker.WorkerState, snapshot []byte) error
	// LoadAll returns every persisted state record.
	LoadAll() ([]StateRecord, error)
	// Delete removes a worker's persisted directory.
	Delete(id string) error
}
