// Package identity is the standalone form's durable bus identity file:
// identities.json, one per project. It implements corebus.Store for the bus
// control plane (core/impl/busctl) and is the one place that knows the file
// format — the runtime store and the one-time migration share it.
package identity

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
)

// Store is a JSON-file corebus.Store: read once on construction,
// rewritten atomically on every mutation, reads served from memory. The file is
// the durable source; there is no second copy.
type Store struct {
	mu   sync.RWMutex
	path string
	ids  map[string]corebus.Identity
}

// newIdentityStore opens (or creates) the identity file at path.
func New(path string) (*Store, error) {
	s := &Store{path: path, ids: map[string]corebus.Identity{}}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("identity store: create dir: %w", err)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity store: read %s: %w", path, err)
	}
	var file identityFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("identity store: parse %s: %w", path, err)
	}
	for _, id := range file.Identities {
		s.ids[id.WorkerID] = id
	}
	return s, nil
}

var _ corebus.Store = (*Store)(nil)

// Load implements corebus.Store.
func (s *Store) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idt, ok := s.ids[id]
	return idt, ok, nil
}

// Save implements corebus.Store.
func (s *Store) Save(id string, idt corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = idt
	return s.persistLocked()
}

// Delete implements corebus.Store.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return s.persistLocked()
}

// All implements corebus.Store.
func (s *Store) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, id := range s.ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

// Merge merges ids into the identity file at path and writes it even
// when the merge is empty. It exists for the one-time migration of legacy
// inline ACLs (and to mark that migration done), so the file format stays owned
// by this package.
func Merge(path string, ids []corebus.Identity) error {
	store, err := New(path)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, id := range ids {
		store.ids[id.WorkerID] = id
	}
	return store.persistLocked()
}

type identityFile struct {
	SchemaVersion int                `json:"schema_version"`
	Identities    []corebus.Identity `json:"identities"`
}

// persistLocked writes the file through a temp file + rename, so a crash never
// leaves half an identity file. The caller holds s.mu.
func (s *Store) persistLocked() error {
	ids := make([]corebus.Identity, 0, len(s.ids))
	for _, id := range s.ids {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].WorkerID < ids[j].WorkerID })
	raw, err := json.MarshalIndent(identityFile{SchemaVersion: 1, Identities: ids}, "", "  ")
	if err != nil {
		return fmt.Errorf("identity store: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0644); err != nil {
		return fmt.Errorf("identity store: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("identity store: rename %s: %w", s.path, err)
	}
	return nil
}
