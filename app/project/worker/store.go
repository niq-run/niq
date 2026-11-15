// Declaration storage boundary for the worker lifecycle. The project owns where
// declarations live and how they are written; the lifecycle only sees this
// interface. The concrete implementation is app/project/store.go.
//
// Runtime state (state.json / snapshots) is NOT declared here: its single
// definition is workerhost.WorkerStore, the contract the in-process lifecycle
// engine calls. The project supplies a concrete implementation.
package worker

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
