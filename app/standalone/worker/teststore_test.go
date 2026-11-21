package worker

import (
	"sort"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
)

// memIdentityStore is a test-only bus.Store: identity persistence is exercised
// by the project's file store, so the control-plane tests only need a map.
type memIdentityStore struct {
	mu  sync.RWMutex
	ids map[string]corebus.Identity
}

func newMemIdentityStore() *memIdentityStore {
	return &memIdentityStore{ids: map[string]corebus.Identity{}}
}

func (s *memIdentityStore) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idt, ok := s.ids[id]
	return idt, ok, nil
}

func (s *memIdentityStore) Save(id string, idt corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = idt
	return nil
}

func (s *memIdentityStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return nil
}

func (s *memIdentityStore) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, idt := range s.ids {
		out = append(out, idt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}
