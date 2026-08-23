package eventbus

import (
	"context"
	"testing"
	"time"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/store"
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