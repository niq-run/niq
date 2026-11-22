package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	projworker "github.com/niq-run/niq/app/standalone/worker"
	"github.com/niq-run/niq/core/impl/workerctl"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// State persists in-process workers' runtime state under a root directory, one
// subdirectory per worker:
//
//	<root>/<workerID>/state.json   — {"state": "...", "snapshot": {…}}
//
// The worker's definition (id/type/params) is not stored here: it lives in the
// manifest store, which hands it back on recovery. The root is the project's
// workers/ dir.
type State struct {
	root string
}

var _ workerctl.WorkerStore = (*State)(nil)

// NewState creates a state store rooted at root (created if missing).
func NewState(root string) (*State, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("store: create state root: %w", err)
	}
	return &State{root: root}, nil
}

func (s *State) dir(id string) string {
	return filepath.Join(s.root, projworker.SanitizeID(id))
}

// SaveState implements workerctl.WorkerStore.
func (s *State) SaveState(id string, state coreworker.WorkerState, snapshot []byte) error {
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", dir, err)
	}
	// Snapshots are stored as raw JSON: a string field would re-escape every
	// quote of the inner JSON (~25% overhead on transcript-heavy snapshots) and
	// make state.json unreadable. Nil/empty is legitimate ("no state").
	if len(snapshot) > 0 && !json.Valid(snapshot) {
		return fmt.Errorf("store: snapshot %s is not valid JSON", id)
	}
	rec := stateFile{State: state, Snapshot: json.RawMessage(snapshot)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("store: marshal state %s: %w", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0644); err != nil {
		return fmt.Errorf("store: write state %s: %w", id, err)
	}
	return nil
}

// LoadAll implements workerctl.WorkerStore.
func (s *State) LoadAll() ([]workerctl.StateRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("store: read state root: %w", err)
	}
	var recs []workerctl.StateRecord
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		state, snapshot, err := readStateFile(filepath.Join(s.root, id, "state.json"))
		if err != nil {
			continue
		}
		recs = append(recs, workerctl.StateRecord{
			ID:       id,
			State:    state,
			Snapshot: snapshot,
		})
	}
	return recs, nil
}

// Delete implements workerctl.WorkerStore.
func (s *State) Delete(id string) error {
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
