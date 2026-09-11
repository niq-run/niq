package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"
)

// fakeEvents returns events sharing the same second timestamp (and even the same
// uuid time prefix) to exercise insertion-order pagination/ordering.
func fakeEvents() []event.Event {
	now := time.Now().Unix()
	return []event.Event{
		{ID: "e1", Type: "worker.ready", Timestamp: now},
		{ID: "e2", Type: "reason.thinking", Timestamp: now}, // same second as e1
		{ID: "e3", Type: "reason.response", Timestamp: now}, // same second
		{ID: "e4", Type: "worker.input", Timestamp: now},    // same second
	}
}

// TestMemoryStoreInsertionOrder asserts pagination preserves insertion order even
// when all events share a second timestamp (the ordering must not be timestamp-,
// id- or random-dependent).
func TestMemoryStoreInsertionOrder(t *testing.T) {
	s := NewMemoryEventStore()
	ctx := context.Background()
	if err := s.Append(ctx, fakeEvents()...); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Ascending (oldest first): must be e1,e2,e3,e4.
	asc, err := s.List(ctx, "*", store.QueryOpts{})
	if err != nil {
		t.Fatal(err)
	}
	wantAsc := []string{"e1", "e2", "e3", "e4"}
	got := ids(asc)
	if !equal(got, wantAsc) {
		t.Fatalf("asc order = %v, want %v", got, wantAsc)
	}

	// Descending (newest first): reversed insertion order e4,e3,e2,e1.
	desc, err := s.List(ctx, "*", store.QueryOpts{Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	wantDesc := []string{"e4", "e3", "e2", "e1"}
	got = ids(desc)
	if !equal(got, wantDesc) {
		t.Fatalf("desc order = %v, want %v", got, wantDesc)
	}

	// Pagination before e3 (all same second): returns e1,e2 (older), not nothing.
	before, err := s.List(ctx, "*", store.QueryOpts{BeforeID: "e3", Desc: true})
	if err != nil {
		t.Fatal(err)
	}
	// Desc before e3 → the two older ones, newest-first: e2, e1.
	if g := ids(before); !equal(g, []string{"e2", "e1"}) {
		t.Fatalf("before e3 = %v, want [e2 e1]", g)
	}
}

// TestMemoryStoreRequestIDFilter asserts the RequestID query option pairs a
// request event with its request.* answer (they share the same request_id).
func TestMemoryStoreRequestIDFilter(t *testing.T) {
	s := NewMemoryEventStore()
	ctx := context.Background()
	now := time.Now().Unix()
	evs := []event.Event{
		{ID: "r1", Type: "bash", WorkerId: "niq", RequestId: "req-1", TraceID: "t1", Timestamp: now},
		{ID: "r2", Type: "request.completed", WorkerId: "ws", RequestId: "req-1", TraceID: "t1", Timestamp: now},
		{ID: "r3", Type: "bash", WorkerId: "niq", RequestId: "req-2", TraceID: "t1", Timestamp: now},
	}
	if err := s.Append(ctx, evs...); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := s.List(ctx, "*", store.QueryOpts{RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	// The invocation AND its request.* answer both match; the other call does not.
	if g := ids(got); !equal(g, []string{"r1", "r2"}) {
		t.Fatalf("request filter = %v, want [r1 r2]", g)
	}

	// No match → empty.
	none, err := s.List(ctx, "*", store.QueryOpts{RequestID: "req-9"})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("request filter no-match = %d events, want 0", len(none))
	}
}

func ids(evs []event.Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.ID
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestShouldPersist(t *testing.T) {
	// Normal conversation events persist.
	if !shouldPersist(event.Event{Type: event.TypeWorkerInput}) {
		t.Fatal("worker.input should persist")
	}
	if !shouldPersist(event.Event{Type: event.TypeRequestCompleted}) {
		t.Fatal("request.completed should persist")
	}
	// Transient is the only thing that keeps an event out of durability;
	// presence/discovery chatter (worker.ready / discover / gone) sets it at
	// the source, so a bare type check here would wrongly persist it.
	if shouldPersist(event.Event{Type: event.TypeWorkerInput, Transient: true}) {
		t.Fatal("transient event should not persist")
	}
	if !shouldPersist(event.Event{Type: event.TypeWorkerReady}) {
		t.Fatal("non-transient event should persist (presence exclusion is set at source)")
	}
}

type recStore struct{ evts []event.Event }
func (r *recStore) Append(_ context.Context, evts ...event.Event) error { r.evts = append(r.evts, evts...); return nil }
func (r *recStore) List(_ context.Context, _ string, _ store.QueryOpts) ([]event.Event, error) { return nil, nil }

func TestTransientRequestReplyPropagation(t *testing.T) {
	st := &recStore{}
	e := NewEngine(nil, st)

	// Transient (human-UI read) request -> not persisted, but its id is noted.
	e.persistEvent(context.Background(), event.Event{Type: "search", RequestId: "R1", Transient: true})
	// Its reply carries no Transient flag but echoes R1 -> also not persisted.
	e.persistEvent(context.Background(), event.Event{Type: event.TypeRequestCompleted, RequestId: "R1"})

	// Non-transient request and its reply DO persist.
	e.persistEvent(context.Background(), event.Event{Type: "search", RequestId: "R2"})
	e.persistEvent(context.Background(), event.Event{Type: event.TypeRequestCompleted, RequestId: "R2"})

	if len(st.evts) != 2 {
		t.Fatalf("want 2 persisted (non-transient request+reply), got %d: %+v", len(st.evts), st.evts)
	}
	if st.evts[0].RequestId != "R2" || st.evts[1].RequestId != "R2" {
		t.Fatalf("expected only the non-transient pair persisted, got %+v", st.evts)
	}
}
