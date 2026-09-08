package api

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/eventbus"
)

// TestFollowLiveWatermarkForNonMatchingLatest verifies that a filter which does
// NOT match the most recent persisted event still gets a filter-agnostic
// watermark anchor. Without it the client can't page history backward and shows
// an empty timeline for historical matches (an old trace, or a completed
// request_id pair).
func TestFollowLiveWatermarkForNonMatchingLatest(t *testing.T) {
	store := eventbus.NewMemoryEventStore()
	now := int64(1000)
	// The newest event carries a different request_id than the one we filter on;
	// it also wins the "latest" slot by being appended last.
	evs := []event.Event{
		{ID: "old-invocation", Type: "bash", WorkerId: "niq", RequestId: "req-1", Timestamp: now},
		{ID: "old-reply", Type: event.TypeRequestCompleted, WorkerId: "ws", RequestId: "req-1", Timestamp: now},
		{ID: "latest-other", Type: "reason.response", WorkerId: "niq", RequestId: "req-9", Timestamp: now},
	}
	if err := store.Append(context.Background(), evs...); err != nil {
		t.Fatalf("append: %v", err)
	}

	l := NewEventLog(nil, store)
	_, wmID, gapEvt, err := l.FollowLive(context.Background(), Filter{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("FollowLive: %v", err)
	}

	// The watermark anchor must be the newest persisted event even though it
	// doesn't match the filter — otherwise no history is paged for req-1.
	if wmID != "latest-other" {
		t.Fatalf("watermark id = %q, want latest-other (filter-agnostic anchor)", wmID)
	}
	// The newest event does not belong to the req-1 view, so no gap event is
	// delivered.
	if gapEvt.ID != "" {
		t.Fatalf("gap event id = %q, want empty (latest event doesn't match filter)", gapEvt.ID)
	}
}

// TestListByRequest verifies the one-shot request_id lookup returns the
// invocation together with whichever request.* reply echoed the id — the data
// behind the detail panel's "Jump to paired request/response" button.
func TestListByRequest(t *testing.T) {
	store := eventbus.NewMemoryEventStore()
	now := int64(1000)
	evts := []event.Event{
		{ID: "inv-1", Type: "bash", WorkerId: "niq", RequestId: "req-1", Timestamp: now},
		{ID: "prog-1", Type: "request.progressed", WorkerId: "ws", RequestId: "req-1", Timestamp: now},
		{ID: "comp-1", Type: event.TypeRequestCompleted, WorkerId: "ws", RequestId: "req-1", Timestamp: now},
		{ID: "inv-2", Type: "read", WorkerId: "niq", RequestId: "req-2", Timestamp: now},
	}
	if err := store.Append(context.Background(), evts...); err != nil {
		t.Fatalf("append: %v", err)
	}

	l := NewEventLog(nil, store)
	got, err := l.ListByRequest(context.Background(), "req-1")
	if err != nil {
		t.Fatalf("ListByRequest: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range got {
		if e.RequestId != "req-1" {
			t.Fatalf("event %s has request_id %q, want req-1", e.ID, e.RequestId)
		}
		ids[e.ID] = true
	}
	// The invocation, its progress, and its terminal answer all share req-1; the
	// unrelated req-2 call must not appear.
	if !ids["inv-1"] || !ids["prog-1"] || !ids["comp-1"] || ids["inv-2"] {
		t.Fatalf("ListByRequest(req-1) ids = %v", ids)
	}

	empty, err := l.ListByRequest(context.Background(), "")
	if err != nil {
		t.Fatalf("ListByRequest empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("ListByRequest(empty) = %d events, want 0", len(empty))
	}
}
