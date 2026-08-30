// Package store defines the read-side interface for event persistence.
// Workers query events via this interface; writes happen on the bus side.
package store

import (
	"context"

	"github.com/niq-run/niq/core/event"
)

// EventStore is the read-side interface for querying persisted events.
// Workers consume this to replay history or display past interactions.
type EventStore interface {
	// List returns events for a single worker, filtered by opts.
	// Use Desc=true for display (newest first), Desc=false for replay (oldest first).
	// When AfterID is set, only events after that event are returned (for rebuild).
	List(ctx context.Context, workerID string, opts QueryOpts) ([]event.Event, error)
}

// Append writes events to the store. Workers should not call this directly;
// the bus injects events during routing. To support pluggable backends
// (in-memory, SQLite, etc.) through a single interface, this lives alongside
// the read methods rather than on a separate type.
type AppendStore interface {
	EventStore

	// Append persists one or more events.
	Append(ctx context.Context, events ...event.Event) error
}

// QueryOpts controls the results returned by [EventStore.List].
type QueryOpts struct {
	Limit     int      // max events to return; 0 means unlimited
	Since     int64    // Unix timestamp; only events at or after this time
	AfterID   string   // only events after this event ID (for replay / rebuild)
	BeforeID  string   // only events before this event ID (for pagination)
	WorkerIDs []string // filter by worker ID set (overrides List's workerID arg)
	TraceID   string   // filter by trace ID
	Desc      bool     // true = newest first (display), false = oldest first (replay)
}
