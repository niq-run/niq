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
