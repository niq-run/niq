package eventbus

import (
	"fmt"
	"sort"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// MemoryIdentityRegistry is the runtime identity table: an in-memory
// corebus.IdentityRegistry with no persistence. The assembly seeds it from the
// durable identity store at startup, and callers that must survive a restart
// write through the bus control plane instead of relying on this registry.
type MemoryIdentityRegistry struct {
	mu         sync.RWMutex
	identities map[string]corebus.Identity
}

// NewMemoryIdentityRegistry creates an empty in-memory registry.
func NewMemoryIdentityRegistry() *MemoryIdentityRegistry {
	return &MemoryIdentityRegistry{identities: make(map[string]corebus.Identity)}
}

func (r *MemoryIdentityRegistry) Register(id corebus.Identity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.identities[id.WorkerID]; exists {
		return fmt.Errorf("eventbus: identity %s already registered", id.WorkerID)
	}
	r.identities[id.WorkerID] = id
	return nil
}

func (r *MemoryIdentityRegistry) Update(workerID string, pubAllow []event.PublishPattern, subAllow []event.EventPattern) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.identities[workerID]
	if !ok {
		return fmt.Errorf("eventbus: identity %s not found", workerID)
	}
	entry.PublishAllow = pubAllow
	entry.SubscribeAllow = subAllow
	r.identities[workerID] = entry
	return nil
}

func (r *MemoryIdentityRegistry) Revoke(workerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.identities[workerID]; !ok {
		return fmt.Errorf("eventbus: identity %s not found", workerID)
	}
	delete(r.identities, workerID)
	return nil
}

func (r *MemoryIdentityRegistry) Lookup(workerID string) (corebus.Identity, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.identities[workerID]
	return id, ok
}

func (r *MemoryIdentityRegistry) List() []corebus.Identity {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(r.identities))
	for _, id := range r.identities {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out
}

var _ corebus.IdentityRegistry = (*MemoryIdentityRegistry)(nil)
