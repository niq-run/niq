// Worker persistence for the assembly layer: the concrete WorkerStore
// implementation, owned by swarm (the layer that decides the storage layout).
// workerhost only depends on the WorkerStore interface.
package swarm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/pkg/service/workerhost"
)

// FileWorkerStore persists workers under a root directory, one subdirectory
// per worker:
//
//	<root>/<workerID>/config.json   — definition (authoritative)
//	<root>/<workerID>/state.json    — {"state": "...", "snapshot": "<raw text>"}
//
// The root is the project's workers/ dir, so config.json doubles as the
// authoritative per-worker definition read by the assembly layer.
type FileWorkerStore struct {
	root string
}

// NewFileWorkerStore creates a store rooted at root (created if missing).
func NewFileWorkerStore(root string) (*FileWorkerStore, error) {
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, fmt.Errorf("workerhost: create store root: %w", err)
	}
	return &FileWorkerStore{root: root}, nil
}

func (s *FileWorkerStore) dir(id string) string {
	return filepath.Join(s.root, sanitizeID(id))
}

func (s *FileWorkerStore) SaveConfig(cfg worker.WorkerConfig) error {
	dir := s.dir(cfg.ID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("workerhost: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("workerhost: marshal config %s: %w", cfg.ID, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0644); err != nil {
		return fmt.Errorf("workerhost: write config %s: %w", cfg.ID, err)
	}
	return nil
}

func (s *FileWorkerStore) SaveState(id string, state worker.WorkerState, snapshot []byte) error {
	dir := s.dir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("workerhost: mkdir %s: %w", dir, err)
	}
	// The snapshot is stored as a raw string, not base64: every worker snapshot
	// is text (JSON transcripts) and staying raw keeps state.json readable.
	rec := stateFile{State: state, Snapshot: string(snapshot)}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("workerhost: marshal state %s: %w", id, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), data, 0644); err != nil {
		return fmt.Errorf("workerhost: write state %s: %w", id, err)
	}
	return nil
}

func (s *FileWorkerStore) LoadAll() ([]workerhost.WorkerRecord, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("workerhost: read store root: %w", err)
	}
	var recs []workerhost.WorkerRecord
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		id := de.Name()
		cfg, err := readConfigFile(filepath.Join(s.root, id, "config.json"))
		if err != nil {
			// Skip unreadable workers rather than failing the whole load.
			continue
		}
		state, snapshot, _ := readStateFile(filepath.Join(s.root, id, "state.json"))
		recs = append(recs, workerhost.WorkerRecord{
			ID:       cfg.ID,
			Type:     cfg.Type,
			Params:   cfg.Params,
			State:    state,
			Snapshot: snapshot,
		})
	}
	return recs, nil
}

func (s *FileWorkerStore) Delete(id string) error {
	return os.RemoveAll(s.dir(id))
}

type stateFile struct {
	State    worker.WorkerState `json:"state"`
	Snapshot string             `json:"snapshot,omitempty"`
}

func readConfigFile(path string) (worker.WorkerConfig, error) {
	var cfg worker.WorkerConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func readStateFile(path string) (worker.WorkerState, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return worker.StateRunning, nil, err
	}
	var rec stateFile
	if err := json.Unmarshal(data, &rec); err != nil {
		return worker.StateRunning, nil, err
	}
	return rec.State, []byte(rec.Snapshot), nil
}
