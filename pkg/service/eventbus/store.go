package eventbus

import (
	"context"
	"sync"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"
)

// MemoryEventStore implements both store.AppendStore and store.EventStore
// using an in-memory slice. It is the simplest possible event store,
// suitable for development and single-process deployments.
type MemoryEventStore struct {
	mu     sync.RWMutex
	events []event.Event
}

// NewMemoryEventStore creates an empty in-memory event store.
func NewMemoryEventStore() *MemoryEventStore {
	return &MemoryEventStore{
		events: make([]event.Event, 0, 256),
	}
}

// Append implements store.AppendStore.
func (s *MemoryEventStore) Append(ctx context.Context, events ...event.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, events...)
	return nil
}

// List implements store.EventStore.
func (s *MemoryEventStore) List(ctx context.Context, workerID string, opts store.QueryOpts) ([]event.Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Pagination by insertion order (index into s.events): events are appended as
	// they occur, so this is the deterministic chronological order — same rationale
	// as the SQLite store's rowid ordering.
	afterIdx, beforeIdx := -1, -1
	if opts.AfterID != "" {
		for i, e := range s.events {
			if e.ID == opts.AfterID {
				afterIdx = i
				break
			}
		}
	}
	if opts.BeforeID != "" {
		for i, e := range s.events {
			if e.ID == opts.BeforeID {
				beforeIdx = i
				break
			}
		}
	}

	var result []event.Event
	allWorkers := workerID == "*"
	for idx, e := range s.events {
		idOK := allWorkers || e.WorkerId == workerID || e.TargetWorkerID == workerID || workerInRecipients(e, workerID)
		if len(opts.WorkerIDs) > 0 {
			idOK = workerMatchesAny(e, opts.WorkerIDs)
		}
		if !idOK {
			continue
		}
		if opts.TraceID != "" && e.TraceID != opts.TraceID {
			continue
		}
		if opts.Since > 0 && e.Timestamp < opts.Since {
			continue
		}
		// Insertion-order pagination: strictly after/before the anchor index.
		if afterIdx >= 0 && idx <= afterIdx {
			continue
		}
		if beforeIdx >= 0 && idx >= beforeIdx {
			continue
		}
		result = append(result, e)
	}

	if opts.Desc {
		for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
			result[i], result[j] = result[j], result[i]
		}
	}

	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// workerInRecipients reports whether the given worker ID appears in the
// event's Recipients list. Recipients is populated by the Engine during
// routing to indicate which workers received the event.
func workerInRecipients(e event.Event, id string) bool {
	for _, r := range e.Recipients {
		if r == id {
			return true
		}
	}
	return false
}

// workerMatchesAny reports whether the event involves any of the given worker
// IDs, as source, target, or recipient.
func workerMatchesAny(e event.Event, ids []string) bool {
	for _, id := range ids {
		if e.WorkerId == id || e.TargetWorkerID == id || workerInRecipients(e, id) {
			return true
		}
	}
	return false
}

// Compile-time checks.
var _ store.AppendStore = (*MemoryEventStore)(nil)
var _ store.EventStore = (*MemoryEventStore)(nil)
