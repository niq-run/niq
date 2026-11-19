// Package busctl implements the bus identity control plane: it owns the
// durable identity records (who may say what) and projects every change onto
// the runtime registry the engine routes through.
//
// It is orthogonal to the worker control plane (app/standalone/worker): one
// answers "which worker exists and is running", the other "who may publish and
// subscribe". The assembly wires the two together — a worker's create/destroy
// registers/revokes its identity — but neither imports the other.
package busctl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// ControlPlane implements corebus.Control over an injected store and keeps a
// runtime registry in step with it. The store is the single durable owner; the
// registry is the live projection the engine and transport read.
type ControlPlane struct {
	store   corebus.Store
	runtime corebus.IdentityRegistry
}

// New creates a control plane over store. runtime is the projection target and
// may be nil (a caller that only wants durable bookkeeping).
func New(store corebus.Store, runtime corebus.IdentityRegistry) *ControlPlane {
	return &ControlPlane{store: store, runtime: runtime}
}

var _ corebus.Control = (*ControlPlane)(nil)

// Seed projects every durable identity onto the runtime registry. It runs once
// at startup, before the bus serves connections, so a remote worker can
// authenticate on its first reconnect instead of waiting to be re-provisioned.
func (c *ControlPlane) Seed() error {
	if c.runtime == nil {
		return nil
	}
	ids, err := c.store.All()
	if err != nil {
		return fmt.Errorf("busctl: seed: %w", err)
	}
	for _, id := range ids {
		if awaitingLaunch(id) {
			continue
		}
		if err := project(c.runtime, id); err != nil {
			return fmt.Errorf("busctl: seed %s: %w", id.WorkerID, err)
		}
	}
	return nil
}

// Register implements corebus.Control.
func (c *ControlPlane) Register(id corebus.Identity) error {
	if id.WorkerID == "" {
		return fmt.Errorf("busctl: identity has no worker id")
	}
	if _, ok, err := c.store.Load(id.WorkerID); err != nil {
		return err
	} else if ok {
		return fmt.Errorf("busctl: identity %s already registered", id.WorkerID)
	}
	if err := c.store.Save(id.WorkerID, id); err != nil {
		return err
	}
	if c.runtime != nil && !awaitingLaunch(id) {
		return project(c.runtime, id)
	}
	return nil
}

// SetCredential implements corebus.Control.
func (c *ControlPlane) SetCredential(workerID, credential string) error {
	id, ok, err := c.store.Load(workerID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("busctl: identity %s not found", workerID)
	}
	id.Credential = credential
	if err := c.store.Save(workerID, id); err != nil {
		return err
	}
	if c.runtime != nil {
		return project(c.runtime, id)
	}
	return nil
}

// awaitingLaunch reports whether id is a host-launched child's durable record:
// it reserves the grants, but its credential is minted per launch into the
// temporary tier and never persisted. Such a record is not projected onto the
// runtime registry until the launch provisions it.
func awaitingLaunch(id corebus.Identity) bool {
	return id.Remote && id.Credential == ""
}

// UpdateACL implements corebus.Control.
func (c *ControlPlane) UpdateACL(workerID string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
	id, ok, err := c.store.Load(workerID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("busctl: identity %s not found", workerID)
	}
	id.PublishAllow = publish
	id.SubscribeAllow = subscribe
	if err := c.store.Save(workerID, id); err != nil {
		return err
	}
	if c.runtime != nil {
		// A declared-but-never-connected worker has no runtime entry; the next
		// connect seeds it from the store.
		if _, ok := c.runtime.Lookup(workerID); ok {
			return c.runtime.Update(workerID, publish, subscribe)
		}
	}
	return nil
}

// Revoke implements corebus.Control. An unknown id is a no-op for the store,
// but the runtime projection is still cleared: a temporary or legacy identity
// can exist without a durable record, and a destroy must not leave it routable.
func (c *ControlPlane) Revoke(workerID string) error {
	if _, ok, err := c.store.Load(workerID); err != nil {
		return err
	} else if ok {
		if err := c.store.Delete(workerID); err != nil {
			return err
		}
	}
	if c.runtime != nil {
		if _, ok := c.runtime.Lookup(workerID); ok {
			return c.runtime.Revoke(workerID)
		}
	}
	return nil
}

// Lookup implements corebus.Control.
func (c *ControlPlane) Lookup(workerID string) (corebus.Identity, bool) {
	id, ok, err := c.store.Load(workerID)
	if err != nil {
		return corebus.Identity{}, false
	}
	return id, ok
}

// List implements corebus.Control.
func (c *ControlPlane) List() []corebus.Identity {
	ids, err := c.store.All()
	if err != nil {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].WorkerID < ids[j].WorkerID })
	return ids
}

// project makes the runtime registry match id, replacing an entry whose
// credential or remote flag changed (a changed credential invalidates the
// connection the old one authenticated).
func project(runtime corebus.IdentityRegistry, id corebus.Identity) error {
	cur, ok := runtime.Lookup(id.WorkerID)
	if !ok {
		return runtime.Register(id)
	}
	if cur.Credential != id.Credential || cur.Remote != id.Remote {
		if err := runtime.Revoke(id.WorkerID); err != nil {
			return err
		}
		return runtime.Register(id)
	}
	return runtime.Update(id.WorkerID, id.PublishAllow, id.SubscribeAllow)
}

// ── file store ──

// FileStore is a JSON-file corebus.Store: read once on construction, rewritten
// atomically on every mutation, reads served from memory. The file is the
// durable source; there is no second copy.
type FileStore struct {
	mu   sync.RWMutex
	path string
	ids  map[string]corebus.Identity
}

// NewFileStore opens (or creates) the identity file at path.
func NewFileStore(path string) (*FileStore, error) {
	s := &FileStore{path: path, ids: map[string]corebus.Identity{}}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("busctl: create identity dir: %w", err)
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("busctl: read %s: %w", path, err)
	}
	var file identityFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("busctl: parse %s: %w", path, err)
	}
	for _, id := range file.Identities {
		s.ids[id.WorkerID] = id
	}
	return s, nil
}

var _ corebus.Store = (*FileStore)(nil)

// Load implements corebus.Store.
func (s *FileStore) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idt, ok := s.ids[id]
	return idt, ok, nil
}

// Save implements corebus.Store.
func (s *FileStore) Save(id string, idt corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = idt
	return s.persistLocked()
}

// Delete implements corebus.Store.
func (s *FileStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return s.persistLocked()
}

// All implements corebus.Store.
func (s *FileStore) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, id := range s.ids {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

// WriteFile merges ids into the identity file at path, in the store's on-disk
// format, and writes the file even when the merge is empty. It exists for the
// one-time migration of legacy inline ACLs (and for marking that migration
// done), so the file format stays owned by this package.
func WriteFile(path string, ids []corebus.Identity) error {
	store, err := NewFileStore(path)
	if err != nil {
		return err
	}
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
func (s *FileStore) persistLocked() error {
	ids := make([]corebus.Identity, 0, len(s.ids))
	for _, id := range s.ids {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].WorkerID < ids[j].WorkerID })
	raw, err := json.MarshalIndent(identityFile{SchemaVersion: 1, Identities: ids}, "", "  ")
	if err != nil {
		return fmt.Errorf("busctl: marshal identities: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0644); err != nil {
		return fmt.Errorf("busctl: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("busctl: rename %s: %w", s.path, err)
	}
	return nil
}
