// Package api provides the bus event query and streaming API.
//
// It is the external interface for observing the event bus — both
// real-time events and historical data. The API is consumed by the
// WebUI, CLI tools, or any other observer that needs visibility into
// the system's event flow.
package api

import (
	"context"
	"log"
	"slices"
	"sync"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"
	"github.com/niq-run/niq/pkg/eventbus"
)

// Filter controls which events a subscriber receives.
type Filter struct {
	WorkerIDs []string        // filter by source, target, or recipient worker
	// WorkerRoles narrows which side of the worker's traffic WorkerIDs
	// matches: store.RoleSent (source) / store.RoleReceived (target or
	// recipient). Empty means both.
	WorkerRoles []string
	TraceID     string          // filter by trace ID
	Type        event.EventType // filter by event type (exact match)
}

// matchesFilter checks whether an event satisfies the filter.
// An empty filter field matches everything.
func matchesFilter(evt event.Event, f Filter) bool {
	if len(f.WorkerIDs) > 0 && !workerMatchesAny(evt, f.WorkerIDs, f.WorkerRoles) {
		return false
	}
	if f.TraceID != "" && evt.TraceID != f.TraceID {
		return false
	}
	if f.Type != "" && evt.Type != f.Type {
		return false
	}
	return true
}

// workerMatchesAny reports whether the event involves any of the given worker
// IDs on at least one of the requested roles (sent = source, received =
// target or recipient; empty roles means both).
func workerMatchesAny(evt event.Event, ids, roles []string) bool {
	sent := len(roles) == 0 || slices.Contains(roles, store.RoleSent)
	received := len(roles) == 0 || slices.Contains(roles, store.RoleReceived)
	for _, id := range ids {
		if sent && evt.WorkerId == id {
			return true
		}
		if received && (evt.TargetWorkerID == id || slices.Contains(evt.Recipients, id)) {
			return true
		}
	}
	return false
}

// subscriber represents a single SSE subscriber watching the event stream.
type subscriber struct {
	filter Filter
	ch     chan event.Event
}

// EventLog provides real-time event streaming and historical query.
// It attaches to an Engine via Hook and provides the data source for the API server.
type EventLog struct {
	engine      *eventbus.Engine
	store       store.EventStore
	mu          sync.Mutex
	subscribers []*subscriber
}

// NewEventLog creates an EventLog attached to the given Engine and store.
// The store is used for historical queries; the engine provides real-time events.
func NewEventLog(engine *eventbus.Engine, store store.EventStore) *EventLog {
	return &EventLog{
		engine: engine,
		store:  store,
	}
}

// Subscribe registers a new subscriber for real-time events.
// Returns a channel that receives events matching the filter, and a cancel function.
// The caller must call cancel when done to clean up resources.
func (l *EventLog) Subscribe(ctx context.Context, filter Filter) (<-chan event.Event, error) {
	ch := make(chan event.Event, 256)

	l.mu.Lock()
	sub := &subscriber{filter: filter, ch: ch}
	l.subscribers = append(l.subscribers, sub)
	l.mu.Unlock()

	// Auto-remove on context cancellation.
	go func() {
		<-ctx.Done()
		l.mu.Lock()
		for i, s := range l.subscribers {
			if s == sub {
				l.subscribers = append(l.subscribers[:i], l.subscribers[i+1:]...)
				break
			}
		}
		l.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

// pushEvent pushes an event to all matching subscribers.
// Called by the Engine after routing each event.
func (l *EventLog) pushEvent(evt event.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, sub := range l.subscribers {
		if !matchesFilter(evt, sub.filter) {
			continue
		}
		select {
		case sub.ch <- evt:
		default:
			log.Printf("[eventbus] api: dropping event for slow subscriber")
		}
	}
}

// Hook returns an event callback that pushes events to the EventLog.
// Attach to the Engine's routing path so every routed event is also logged.
func (l *EventLog) Hook() func(event.Event) {
	return func(evt event.Event) {
		l.pushEvent(evt)
	}
}

// Follow returns a channel of events matching the filter.
// It first replays history from the store, then seamlessly switches to
// real-time delivery via the subscriber mechanism.
// FollowLive returns a channel of real-time events only — no history replay.
// It also returns a watermark: the ID of the newest event persisted in the
// store at the moment of subscription. The caller is expected to page backwards
// from that watermark (via LoadBefore) to fetch history.
//
// LoadBefore is strictly-before, so the watermark event itself would fall
// through the crack — in no history page and not newer than the watermark.
// It is therefore forwarded on the live channel: events strictly older than
// the watermark are skipped (they are in the store and page in via history),
// while the watermark event arrives exactly once via live.
func (l *EventLog) FollowLive(ctx context.Context, filter Filter) (<-chan event.Event, string, error) {
	// Subscription boundary: the newest event ID currently in the store.
	// Event IDs are time-ordered UUIDv7, so this is a safe monotonic watermark
	// even across filter boundaries.
	watermark := ""
	if latest, err := l.store.List(ctx, "*", store.QueryOpts{Limit: 1, Desc: true}); err == nil && len(latest) > 0 {
		watermark = latest[0].ID
	}

	liveCh, err := l.Subscribe(ctx, filter)
	if err != nil {
		return nil, "", err
	}

	out := make(chan event.Event, 256)
	go func() {
		defer close(out)
		for evt := range liveCh {
			if !matchesFilter(evt, filter) {
				continue
			}
			// Skip events already covered by history paging (LoadBefore is
			// strictly-before the watermark, so everything older than it is
			// (or will be) paged in by the client. The watermark event itself
			// is NOT in any history page — forward it, this is its only
			// delivery path.
			if watermark != "" && evt.ID < watermark {
				continue
			}
			select {
			case out <- evt:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, watermark, nil
}

// LoadBefore returns events older than the given anchor event ID.
func (l *EventLog) LoadBefore(ctx context.Context, filter Filter, anchor string, limit int) ([]event.Event, error) {
	if limit <= 0 {
		limit = 50
	}
	return l.store.List(ctx, "*", store.QueryOpts{
		BeforeID:    anchor,
		Limit:       limit,
		Desc:        true,
		WorkerIDs:   filter.WorkerIDs,
		WorkerRoles: filter.WorkerRoles,
		TraceID:     filter.TraceID,
	})
}
