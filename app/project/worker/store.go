// Storage boundaries for the worker lifecycle. Both interfaces live here, next
// to the lifecycle that drives them; the concrete project layout lives in
// app/project/store.go and is injected by the assembly layer.
package worker

import (
	"github.com/niq-run/niq/core/impl/workerhost"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// DeclStore is the declaration storage the lifecycle drives. It is the only
// place that knows WHERE declarations are written; the lifecycle only asks
// what is declared and records changes.
type DeclStore interface {
	// Get returns one declaration. ok is false when the id is not declared.
	Get(id string) (*Declaration, bool, error)
	// List returns every declaration, in declaration order.
	List() ([]*Declaration, error)
	// Add records a new declaration. Declaring an existing id is an error: the
	// lifecycle checks for duplicates so it can report them precisely.
	Add(d *Declaration) error
	// Update replaces an existing declaration; an unknown id is an error.
	Update(d *Declaration) error
	// Delete removes a declaration; an unknown id is a no-op.
	Delete(id string) error
	// SetCredential persists a generated bus credential onto a declaration, so
	// an unmanaged-process worker reuses it across restarts.
	SetCredential(id, credential string) error
	// RemoveState deletes a worker's persisted state directory (snapshot,
	// stdout log, third-party state dir). A missing directory is a no-op.
	RemoveState(id string) error
}

// StateRecord is the persisted runtime half of an in-process worker. It is the
// core workerhost record; the interface below is the project's injection point.
type StateRecord = workerhost.StateRecord

// StateStore persists in-process workers' lifecycle state and snapshots. The
// in-process lifecycle (core/impl/workerhost.WorkerService) calls SaveState on
// transitions and LoadAll on recovery; app/project supplies the concrete
// filesystem layout through BackendConfig.StateStore.
type StateStore interface {
	// SaveState writes the worker's lifecycle state and latest snapshot.
	SaveState(id string, state coreworker.WorkerState, snapshot []byte) error
	// LoadAll returns every persisted state record.
	LoadAll() ([]StateRecord, error)
	// Delete removes a worker's persisted state directory.
	Delete(id string) error
}

// Structural compatibility: the state store interface is exactly what
// WorkerService.SetStore expects.
var _ workerhost.WorkerStore = StateStore(nil)
