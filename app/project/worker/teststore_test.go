package worker

import (
	"sync"

	"github.com/niq-run/niq/core/impl/workerhost"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// memStore is the in-memory WorkerStore used by this package's tests. The
// concrete filesystem store belongs to app/project; the worker subsystem only
// depends on the interface.
type memStore struct {
	mu   sync.Mutex
	recs map[string]workerhost.StateRecord
}

func newMemStore() *memStore {
	return &memStore{recs: map[string]workerhost.StateRecord{}}
}

func (m *memStore) SaveState(id string, state coreworker.WorkerState, snapshot []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs[id] = workerhost.StateRecord{ID: id, State: state, Snapshot: append([]byte(nil), snapshot...)}
	return nil
}

func (m *memStore) LoadAll() ([]workerhost.StateRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]workerhost.StateRecord, 0, len(m.recs))
	for _, r := range m.recs {
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.recs, id)
	return nil
}
