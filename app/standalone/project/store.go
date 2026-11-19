// Project storage for the worker control plane: manifests (workers.json /
// workers.archived.json) and runtime state (workers/<id>/state.json). The
// control plane sees only the ManifestStore and workerlife.WorkerStore
// interfaces; this file owns the concrete project layout.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	projworker "github.com/niq-run/niq/app/standalone/worker"
	"github.com/niq-run/niq/core/impl/workerlife"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// manifestStore implements worker.ManifestStore over one project's worker
// files. Every call re-reads the file: manifests are edited by the WebUI, by a
// runtime spawn and by this store, and a cached copy would let one writer
// silently revert another.
type manifestStore struct {
	projectID string
}

var _ projworker.ManifestStore = manifestStore{}

func (s manifestStore) Get(id string) (*projworker.Manifest, bool, error) {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return nil, false, err
	}
	m, ok := FindWorker(set, id)
	if !ok {
		return nil, false, nil
	}
	return &m, true, nil
}

func (s manifestStore) List(archived bool) ([]*projworker.Manifest, error) {
	set, err := loadSetFor(s.projectID, archived)
	if err != nil {
		return nil, err
	}
	out := make([]*projworker.Manifest, 0, len(set.Workers))
	for i := range set.Workers {
		m := set.Workers[i]
		out = append(out, &m)
	}
	return out, nil
}

func (s manifestStore) Add(m *projworker.Manifest) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	if _, ok := FindWorker(set, m.ID); ok {
		return fmt.Errorf("worker %s already exists", m.ID)
	}
	set.Workers = append(set.Workers, *m)
	return SaveWorkers(s.projectID, set)
}

func (s manifestStore) Update(m *projworker.Manifest) error {
	set, err := LoadWorkers(s.projectID)
	if err != nil {
		return err
	}
	for i := range set.Workers {
		if set.Workers[i].ID == m.ID {
			set.Workers[i] = *m
			return SaveWorkers(s.projectID, set)
		}
	}
	return fmt.Errorf("worker %s is not declared", m.ID)
}

func (s manifestStore) Delete(id string) error {
	return RemoveWorkerDecl(s.projectID, id)
}

func (s manifestStore) Archive(id string, archived bool) error {
	return SetWorkerArchived(s.projectID, id, archived)
}

// StateStore persists in-process workers' runtime state under a root
// directory, one subdirectory per worker:
//
//	<root>/<workerID>/state.json   — {"state": "...", "snapshot": {…}}
//
// The worker's definition (id/type/params) is not stored here: it lives in the
// manifest store, which hands it back on recovery. The root is the project's
// workers/ dir.
type StateStore struct {
	root string
}

var _ workerlife.WorkerStore = (*StateStore)(nil)

// NewStateStore creates a store rooted at root (created if missing).
func NewStateStore(root string) (*StateStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("project: create store root: %w", err)
	}
	return &StateStore{root: root}, nil
}

func (s *StateStore) dir(id string) string {
	return filepath.Join(s.root, sanitizeID(id))
}

// SaveState implements workerlife.WorkerStore.
func (s *StateStore) SaveState(id string, state coreworker.WorkerState, snapshot []byte) error {
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("project: mkdir %s: %w", dir, err)
	}
	// Snapshots are stored as raw JSON: a string field would re-escape every
	// quote of the inner JSON (~25% overhead on transcript-heavy snapshots) and
	// make state.json unreadable. Nil/empty is legitimate ("no state").
	if len(snapshot) > 0 && !json.Valid(snapshot) {
		return fmt.Errorf("project: snapshot %s is not valid JSON", id)
	}
	rec := stateFile{State: state, Snapshot: json.RawMessage(snapshot)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("project: marshal state %s: %w", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0644); err != nil {
		return fmt.Errorf("project: write state %s: %w", id, err)
	}
	return nil
}

// LoadAll implements workerlife.WorkerStore.
func (s *StateStore) LoadAll() ([]workerlife.StateRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("project: read store root: %w", err)
	}
	var recs []workerlife.StateRecord
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		state, snapshot, err := readStateFile(filepath.Join(s.root, id, "state.json"))
		if err != nil {
			continue
		}
		recs = append(recs, workerlife.StateRecord{
			ID:       id,
			State:    state,
			Snapshot: snapshot,
		})
	}
	return recs, nil
}

// Delete implements workerlife.WorkerStore.
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
