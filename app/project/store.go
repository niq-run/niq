// Project storage for the worker lifecycle: declarations (workers.json) and
// in-process runtime state (workers/<id>/state.json). The lifecycle package sees
// only the DeclStore and workerhost.WorkerStore interfaces; this file owns the
// concrete project layout.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	projworker "github.com/niq-run/niq/app/project/worker"
	"github.com/niq-run/niq/core/impl/workerhost"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// projectDeclStore implements worker.DeclStore over one project's active
// workers.json. Every call re-reads the file: declarations are edited by the
// WebUI, by a runtime spawn and by this store, and a cached copy would let one
// writer silently revert another.
type projectDeclStore struct {
	projectID string
}

var _ projworker.DeclStore = projectDeclStore{}

func (s projectDeclStore) Get(id string) (*projworker.Declaration, bool, error) {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return nil, false, err
	}
	d, ok := FindWorker(set, id)
	if !ok {
		return nil, false, nil
	}
	return &d, true, nil
}

func (s projectDeclStore) List() ([]*projworker.Declaration, error) {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return nil, err
	}
	out := make([]*projworker.Declaration, 0, len(set.Workers))
	for i := range set.Workers {
		d := set.Workers[i]
		out = append(out, &d)
	}
	return out, nil
}

func (s projectDeclStore) Add(d *projworker.Declaration) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	if _, ok := FindWorker(set, d.ID); ok {
		return fmt.Errorf("worker %s already exists", d.ID)
	}
	set.Workers = append(set.Workers, *d)
	return SaveWorkers(s.projectID, set)
}

func (s projectDeclStore) Update(d *projworker.Declaration) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	for i := range set.Workers {
		if set.Workers[i].ID == d.ID {
			set.Workers[i] = *d
			return SaveWorkers(s.projectID, set)
		}
	}
	return fmt.Errorf("worker %s is not declared", d.ID)
}

func (s projectDeclStore) Delete(id string) error {
	return RemoveWorkerDecl(s.projectID, id)
}

func (s projectDeclStore) SetCredential(id, cred string) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	for i := range set.Workers {
		if set.Workers[i].ID == id {
			set.Workers[i].Credential = cred
			return SaveWorkers(s.projectID, set)
		}
	}
	return fmt.Errorf("worker %s is not declared", id)
}

func (s projectDeclStore) RemoveState(id string) error {
	return removeWorkerStateDir(s.projectID, id)
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

var _ workerhost.WorkerStore = (*StateStore)(nil)

// NewStateStore creates a store rooted at root (created if missing).
func NewStateStore(root string) (*StateStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("workerhost: create store root: %w", err)
	}
	return &StateStore{root: root}, nil
}

func (s *StateStore) dir(id string) string {
	return filepath.Join(s.root, sanitizeID(id))
}

// SaveState implements workerhost.WorkerStore.
func (s *StateStore) SaveState(id string, state coreworker.WorkerState, snapshot []byte) error {
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("workerhost: mkdir %s: %w", dir, err)
	}
	// Snapshots are stored as raw JSON: a string field would re-escape every
	// quote of the inner JSON (~25% overhead on transcript-heavy snapshots) and
	// make state.json unreadable. Nil/empty is legitimate ("no state").
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

// LoadAll implements workerhost.WorkerStore.
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
