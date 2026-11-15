// Persistence for the worker lifecycle: where declarations live, and where an
// in-process worker's runtime state (its snapshot) lives.
//
// Declarations are owned by the project (workers.json / workers.archived.json); this package only
// depends on the DeclStore interface, which the project package implements. The
// state store is implemented here because the layout (one directory per worker,
// state.json) is a property of the lifecycle, not of the project file.
package worker

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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

// StateStore persists in-process workers' runtime state under a root
// directory, one subdirectory per worker:
//
//	<root>/<workerID>/state.json   — {"state": "...", "snapshot": {…}}
//
// The worker's definition (id/type/params) is not stored here: it lives in the
// declaration store, which hands it back on recovery. The root is the project's
// workers/ dir.
type StateStore struct {
	root string
}

// NewStateStore creates a store rooted at root (created if missing).
func NewStateStore(root string) (*StateStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("workerhost: create store root: %w", err)
	}
	return &StateStore{root: root}, nil
}

func (s *StateStore) dir(id string) string {
	return filepath.Join(s.root, SanitizeID(id))
}

// SaveState implements workerhost.WorkerStore.
func (s *StateStore) SaveState(id string, state coreworker.WorkerState, snapshot []byte) error {
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("workerhost: mkdir %s: %w", dir, err)
	}
	// The snapshot is stored as raw JSON (json.RawMessage inlines it verbatim),
	// not as a string: a string field re-escapes every quote of the inner JSON
	// (~25% size overhead on transcript-heavy snapshots) and makes state.json
	// unreadable. Worker snapshots are JSON by contract — a non-empty snapshot
	// that isn't valid JSON is a bug and fails loudly here. Nil/empty is
	// legitimate ("no state to persist") and simply omits the field.
	if len(snapshot) > 0 && !json.Valid(snapshot) {
		return fmt.Errorf("workerhost: snapshot %s is not valid JSON", id)
	}
	rec := stateFile{State: state, Snapshot: json.RawMessage(snapshot)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("workerhost: marshal state %s: %w", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0644); err != nil {
		return fmt.Errorf("workerhost: write state %s: %w", id, err)
	}
	return nil
}

// LoadAll implements workerhost.WorkerStore. Workers declared but never started
// have no state.json yet and are simply absent — recovery treats them as fresh
// starts.
func (s *StateStore) LoadAll() ([]workerhost.StateRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("workerhost: read store root: %w", err)
	}
	var recs []workerhost.StateRecord
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		state, snapshot, err := readStateFile(filepath.Join(s.root, id, "state.json"))
		if err != nil {
			continue
		}
		recs = append(recs, workerhost.StateRecord{
			ID:       id,
			State:    state,
			Snapshot: snapshot,
		})
	}
	return recs, nil
}

// Delete implements workerhost.WorkerStore.
func (s *StateStore) Delete(id string) error {
	return os.RemoveAll(s.dir(id))
}

type stateFile struct {
	State    coreworker.WorkerState `json:"state"`
	Snapshot json.RawMessage        `json:"snapshot,omitempty"`
}

func readStateFile(path string) (coreworker.WorkerState, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return coreworker.StateRunning, nil, err
	}
	var rec stateFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return coreworker.StateRunning, nil, err
	}
	return rec.State, []byte(rec.Snapshot), nil
}
