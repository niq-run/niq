// Package eventbus provides the built-in bus engine implementation.
//
// It implements the core protocol interfaces defined in core/bus:
//   - IdentityRegistry (file-backed)
//   - BusSideChannel / WorkerSideChannel (in-process transport)
//   - Event routing (Send + Broadcast) via Engine
package eventbus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// FileIdentityRegistry implements corebus.IdentityRegistry backed by a JSON file.
//
// Identities are persisted to disk so they survive process restarts.
// The file is read once on construction and rewritten on every mutation.
// Reads are served from an in-memory map for performance.
type FileIdentityRegistry struct {
	mu         sync.RWMutex
	path       string
	identities map[string]corebus.Identity
}

// NewFileIdentityRegistry creates a registry backed by the given file path.
// If the file exists, it is loaded. If not, an empty registry is created.
func NewFileIdentityRegistry(path string) (*FileIdentityRegistry, error) {
	r := &FileIdentityRegistry{
		path:       path,
		identities: make(map[string]corebus.Identity),
	}

	// Ensure directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("eventbus: create registry dir: %w", err)
	}

	// Load existing file if present.
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		if err := json.NewDecoder(f).Decode(&r.identities); err != nil {
			return nil, fmt.Errorf("eventbus: load registry: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("eventbus: open registry: %w", err)
	}

	return r, nil
}

// save persists the current identities to the JSON file.
// Must be called with r.mu held.
func (r *FileIdentityRegistry) save() error {
	f, err := os.Create(r.path)
	if err != nil {
		return fmt.Errorf("eventbus: save registry: %w", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.identities); err != nil {
		return fmt.Errorf("eventbus: encode registry: %w", err)
	}
	return nil
}

// Register implements corebus.IdentityRegistry.
func (r *FileIdentityRegistry) Register(id corebus.Identity) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.identities[id.WorkerID]; exists {
		return fmt.Errorf("eventbus: identity %s already registered", id.WorkerID)
	}
	r.identities[id.WorkerID] = id
	return r.save()
}

// Update implements corebus.IdentityRegistry.
func (r *FileIdentityRegistry) Update(workerID string, pubAllow []event.PublishPattern, subAllow []event.EventPattern) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.identities[workerID]
	if !ok {
		return fmt.Errorf("eventbus: identity %s not found", workerID)
	}
	entry.PublishAllow = pubAllow
	entry.SubscribeAllow = subAllow
	r.identities[workerID] = entry
	return r.save()
}

// Revoke implements corebus.IdentityRegistry.
func (r *FileIdentityRegistry) Revoke(workerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.identities[workerID]; !ok {
		return fmt.Errorf("eventbus: identity %s not found", workerID)
	}
	delete(r.identities, workerID)
	return r.save()
}

// Lookup implements corebus.IdentityRegistry.
func (r *FileIdentityRegistry) Lookup(workerID string) (corebus.Identity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.identities[workerID]
	return id, ok
}

// List implements corebus.IdentityRegistry. The backing store is a map, whose
// iteration order is randomized by the Go runtime; sort by WorkerID so callers
// (e.g. the WebUI worker list) get a stable, deterministic order.
func (r *FileIdentityRegistry) List() []corebus.Identity {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]corebus.Identity, 0, len(r.identities))
	for _, id := range r.identities {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].WorkerID < ids[j].WorkerID })
	return ids
}

// Close closes the registry. No-op for file-backed implementation.
// Included for interface completeness.
func (r *FileIdentityRegistry) Close() error {
	return nil
}

// Compile-time check: *FileIdentityRegistry implements IdentityRegistry.
var _ corebus.IdentityRegistry = (*FileIdentityRegistry)(nil)
