package eventbus

// LayeredRegistry layers an in-memory tier over a durable IdentityRegistry.
//
// Its purpose is to host TEMPORARY identities — the third-party processes the
// host itself launches (MCP stdio servers, custom agents). Those are trusted at
// their launch moment, so their identity + token live for the duration of the
// main process: in memory only, never persisted. When the process exits, the
// memory tier (and every temp identity in it) is gone together with the
// children it launched — "temp credential lifetime = main process lifetime".
//
// Durable identities (managed workers, provisioned-with-token remote peers)
// continue to live in the underlying durable registry. Lookup consults memory
// first, then durable; List merges both. Engine, transport server and builders
// all keep using it through the same corebus.IdentityRegistry interface.
//
// A temp id may not shadow a durable one (and vice versa): both Register and
// RegisterTemp reject collisions with the other tier, so id resolution is made
// explicit by which tier "owns" the id rather than an implicit priority.

import (
	"fmt"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// LayeredRegistry is a durable IdentityRegistry with a read-through
// in-memory tier for temporary identities.
type LayeredRegistry struct {
	mu      sync.RWMutex
	mem     map[string]corebus.Identity
	durable corebus.IdentityRegistry
}

// NewLayeredRegistry wraps durable with a memory tier.
func NewLayeredRegistry(durable corebus.IdentityRegistry) *LayeredRegistry {
	return &LayeredRegistry{
		mem:     make(map[string]corebus.Identity),
		durable: durable,
	}
}

// RegisterTemp records an in-memory, process-lifetime identity. Reusing an id
// already present in either tier is an error (explicit id ownership).
func (r *LayeredRegistry) RegisterTemp(id corebus.Identity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.mem[id.WorkerID]; ok {
		return fmt.Errorf("eventbus: temp identity %s already registered", id.WorkerID)
	}
	if _, ok := r.durable.Lookup(id.WorkerID); ok {
		return fmt.Errorf("eventbus: identity %s already registered durably (cannot shadow)", id.WorkerID)
	}
	r.mem[id.WorkerID] = id
	return nil
}

// RevokeTemp removes an in-memory identity. It does not touch the durable tier.
func (r *LayeredRegistry) RevokeTemp(workerID string) {
	r.mu.Lock()
	delete(r.mem, workerID)
	r.mu.Unlock()
}

// Register implements IdentityRegistry: durable registration (refresh handled
// by the caller), refusing when a temp identity already owns the id.
func (r *LayeredRegistry) Register(id corebus.Identity) error {
	r.mu.Lock()
	if _, ok := r.mem[id.WorkerID]; ok {
		r.mu.Unlock()
		return fmt.Errorf("eventbus: identity %s is a temporary identity, cannot register durably", id.WorkerID)
	}
	r.mu.Unlock()
	return r.durable.Register(id)
}

// Update implements IdentityRegistry against whichever tier owns the id.
func (r *LayeredRegistry) Update(workerID string, pubAllow []event.PublishPattern, subAllow []event.EventPattern) error {
	r.mu.Lock()
	if _, ok := r.mem[workerID]; ok {
		entry := r.mem[workerID]
		entry.PublishAllow = pubAllow
		entry.SubscribeAllow = subAllow
		r.mem[workerID] = entry
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	return r.durable.Update(workerID, pubAllow, subAllow)
}

// Revoke implements IdentityRegistry against whichever tier owns the id.
func (r *LayeredRegistry) Revoke(workerID string) error {
	r.mu.Lock()
	if _, ok := r.mem[workerID]; ok {
		delete(r.mem, workerID)
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	return r.durable.Revoke(workerID)
}

// Lookup implements IdentityRegistry: memory tier first, then durable.
func (r *LayeredRegistry) Lookup(workerID string) (corebus.Identity, bool) {
	r.mu.RLock()
	if id, ok := r.mem[workerID]; ok {
		r.mu.RUnlock()
		return id, true
	}
	r.mu.RUnlock()
	return r.durable.Lookup(workerID)
}

// List implements IdentityRegistry: merged, stable (id-sorted) view of both
// tiers.
func (r *LayeredRegistry) List() []corebus.Identity {
	r.mu.RLock()
	out := make([]corebus.Identity, 0, len(r.mem))
	for _, id := range r.mem {
		out = append(out, id)
	}
	r.mu.RUnlock()
	return append(out, r.durable.List()...)
}

// Compile-time check.
var _ corebus.IdentityRegistry = (*LayeredRegistry)(nil)
