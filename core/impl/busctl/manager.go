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
	"fmt"
	"sort"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// Manager implements corebus.Control over an injected store and keeps a
// runtime registry in step with it. The store is the single durable owner; the
// registry is the live projection the engine and transport read.
type Manager struct {
	store   corebus.Store
	runtime corebus.IdentityRegistry
}

// New creates a control plane over store. runtime is the projection target and
// may be nil (a caller that only wants durable bookkeeping).
func New(store corebus.Store, runtime corebus.IdentityRegistry) *Manager {
	return &Manager{store: store, runtime: runtime}
}

var _ corebus.Control = (*Manager)(nil)

// Seed projects every durable identity onto the runtime registry. It runs once
// at startup, before the bus serves connections, so a remote worker can
// authenticate on its first reconnect instead of waiting to be re-provisioned.
//
// exclude names identities that are durable but must NOT be routable (archived
// workers): their grants are kept for a restore, but they are not part of the
// running swarm. The assembly computes the set from the manifest store — the
// control plane stays ignorant of manifests.
func (c *Manager) Seed(exclude []string) error {
	if c.runtime == nil {
		return nil
	}
	skip := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		skip[id] = true
	}
	ids, err := c.store.All()
	if err != nil {
		return fmt.Errorf("busctl: seed: %w", err)
	}
	for _, id := range ids {
		if awaitingLaunch(id) || skip[id.WorkerID] {
			continue
		}
		if err := project(c.runtime, id); err != nil {
			return fmt.Errorf("busctl: seed %s: %w", id.WorkerID, err)
		}
	}
	return nil
}

// Register implements corebus.Control.
func (c *Manager) Register(id corebus.Identity) error {
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
func (c *Manager) SetCredential(workerID, credential string) error {
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
func (c *Manager) UpdateACL(workerID string, publish []event.PublishPattern, subscribe []event.EventPattern) error {
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
	// A write is always reflected live. A host-launched child awaiting its
	// credential is the one exception: its runtime identity is the temporary one
	// created at launch.
	return projectIfRoutable(c.runtime, id)
}

// Project implements corebus.Control.
func (c *Manager) Project(workerID string) error {
	id, ok, err := c.store.Load(workerID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("busctl: identity %s not found", workerID)
	}
	return projectIfRoutable(c.runtime, id)
}

// Revoke implements corebus.Control. An unknown id is a no-op for the store,
// but the runtime projection is still cleared: a temporary or legacy identity
// can exist without a durable record, and a destroy must not leave it routable.
func (c *Manager) Revoke(workerID string) error {
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
func (c *Manager) Lookup(workerID string) (corebus.Identity, bool) {
	id, ok, err := c.store.Load(workerID)
	if err != nil {
		return corebus.Identity{}, false
	}
	return id, ok
}

// List implements corebus.Control.
func (c *Manager) List() []corebus.Identity {
	ids, err := c.store.All()
	if err != nil {
		return nil
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].WorkerID < ids[j].WorkerID })
	return ids
}

// projectIfRoutable projects id unless it is a host-launched child waiting for
// its per-launch credential (that identity belongs to the temporary tier).
func projectIfRoutable(runtime corebus.IdentityRegistry, id corebus.Identity) error {
	if runtime == nil || awaitingLaunch(id) {
		return nil
	}
	return project(runtime, id)
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
