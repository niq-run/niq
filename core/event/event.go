package event

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// EventType identifies an event's type on the bus.
type EventType string

// Universal bus protocol event types. Every worker must use these exact
// names to interoperate — a worker implementing a capability must publish/
// subscribe with the matching event type. Worker-specific domain events
// (e.g. reason.*, timer.*) are not listed here; they are defined by their
// owning worker.
const (
	// Worker presence and lifecycle.
	TypeWorkerReady    EventType = "worker.ready"
	TypeWorkerGone     EventType = "worker.gone"
	TypeWorkerDiscover EventType = "worker.discover"
	TypeWorkerInput    EventType = "worker.input"
	TypeWorkerAbort    EventType = "worker.abort"

	// Worker meta capabilities: update / updated are the update-command →
	// completion-event pair; query / status are the query-command → snapshot
	// pair. The specific operation lives in the payload's "op" field (parallel
	// to tool.request's "name").
	TypeWorkerUpdate EventType = "worker.update"
	TypeWorkerUpdated EventType = "worker.updated"
	TypeWorkerQuery   EventType = "worker.query"
	TypeWorkerStatus  EventType = "worker.status"

	// Tool invocation lifecycle.
	TypeToolRequest  EventType = "tool.request"
	TypeToolCancel   EventType = "tool.cancel"
	TypeToolCompleted EventType = "tool.completed"
	TypeToolFailed    EventType = "tool.failed"
	TypeToolRejected  EventType = "tool.rejected"
	TypeToolPartial   EventType = "tool.partial"
)

// EventStatus represents an event's lifecycle stage.
type EventStatus string

const (
	StatusCreated   EventStatus = "created"
	StatusRouted    EventStatus = "routed"
	StatusDelivered EventStatus = "delivered"
)

// EventPattern is a subscription declaration: events of a type, optionally
// from a specific source worker. It is the single shape used both for the
// bus's SubscribeAllow routing and for a Worker's declared subscriptions.
type EventPattern struct {
	// Type is the event type to match.
	// Supports exact match, "*" (any), and "Prefix.*" (prefix) wildcards.
	Type EventType `json:"type"`
	// SourceID optionally restricts the subscription to events published by
	// this source worker. Empty means "any source".
	SourceID string `json:"source_id,omitempty"`
}

// Matches reports whether a fully-routed event satisfies this subscription.
// Event.WorkerId is set by the bus to the sender, so source matching is
// authoritative regardless of who forwarded the event.
func (p EventPattern) Matches(evt Event) bool {
	if !PatternMatches(string(p.Type), evt.Type) {
		return false
	}
	if p.SourceID != "" && evt.WorkerId != p.SourceID {
		return false
	}
	return true
}

// NewPattern is a convenience constructor for a type-only subscription.
func NewPattern(typ EventType) EventPattern {
	return EventPattern{Type: typ}
}

// PatternMatches reports whether an event type matches a subscription pattern.
// Supports:
//   - "*"            — matches any event type
//   - exact match     — e.g. "tool.completed"
//   - "Prefix.*"      — prefix wildcard, also matches the bare prefix
//     (e.g. "github.*" matches "github" and "github.issue.new")
//
// An empty pattern matches nothing, so an accidental empty subscription fails
// closed instead of silently matching everything. Use "*" for match-all.
//
// This is the single source of truth for subscription matching; both the bus
// (broadcast routing) and workers (event converter dispatch) use it.
func PatternMatches(pattern string, eventType EventType) bool {
	if pattern == "*" {
		return true
	}
	et := string(eventType)
	if pattern == et {
		return true
	}
	if prefix, ok := strings.CutSuffix(pattern, ".*"); ok {
		return et == prefix || strings.HasPrefix(et, prefix+".")
	}
	return false
}

// Event is the core data unit of the niq event bus.
type Event struct {
	ID             string         `json:"id"`
	Type           EventType      `json:"type"`
	Status         EventStatus    `json:"status"`
	Payload        map[string]any `json:"payload"`
	WorkerId       string         `json:"worker_id"`
	TargetWorkerID string         `json:"target_worker_id,omitempty"`
	TraceID        string         `json:"trace_id,omitempty"`
	SpecVersion    string         `json:"specversion,omitempty"`
	DataSchema     string         `json:"dataschema,omitempty"`
	Timestamp      int64          `json:"timestamp"`
	Recipients     []string       `json:"recipients,omitempty"` // populated by engine during routing

	// Transient marks events that are live-streaming UI transients (streaming
	// deltas, partial tool output) rather than durable history. The source sets
	// it; the bus delivers them live to observers but skips them on persistence.
	Transient bool `json:"transient,omitempty"`
}

// New creates a new Event with defaults.
func New(typ EventType, workerId string, payload map[string]any) Event {
	return Event{
		ID:          newID(),
		Type:        typ,
		WorkerId:    workerId,
		Timestamp:   time.Now().Unix(),
		Payload:     payload,
		SpecVersion: "niq/1.0",
		Status:      StatusCreated,
	}
}

// newID returns a time-ordered UUIDv7 string. It is globally unique and
// sortable by generation time, making event IDs suitable as durable keys and
// for chronological audit/replay.
func newID() string {
	id, _ := uuid.NewV7() // never fails in practice; uses crypto/rand
	return id.String()
}
