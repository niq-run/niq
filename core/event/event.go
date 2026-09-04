package event

import (
	"encoding/json"
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

	// Worker meta invocation channels: worker.update / worker.query remain as
	// the event types some extensions register (their op/subject payloads still
	// discriminate), but reason's own meta capabilities now use dedicated event
	// types (see pkg/reason), so these are no longer the only meta channel.
	TypeWorkerUpdate EventType = "worker.update"
	TypeWorkerQuery  EventType = "worker.query"

	// The request-response pairing convention: any invocation of a capability
	// is answered with one of these, echoing the Event.RequestId of the
	// request. request.completed carries the result; request.failed /
	// request.rejected carry the error; request.progressed streams partial
	// results; request.cancel is sent by the caller to cancel a pending one.
	TypeRequestCompleted  EventType = "request.completed"
	TypeRequestFailed     EventType = "request.failed"
	TypeRequestRejected   EventType = "request.rejected"
	TypeRequestProgressed EventType = "request.progressed"
	TypeRequestCancel     EventType = "request.cancel"
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
	SourceID string `json:"source,omitempty"`
}

// UnmarshalJSON accepts the canonical "source" spelling and the legacy
// "source_id" (the pre-unification tag, still present in persisted registry
// files). Without this, an old source restriction would silently drop — a
// silent WIDENING of what the worker receives.
func (p *EventPattern) UnmarshalJSON(b []byte) error {
	type alias EventPattern
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*p = EventPattern(a)
	var legacy struct {
		SourceID *string `json:"source_id"`
	}
	if err := json.Unmarshal(b, &legacy); err != nil {
		return err
	}
	if legacy.SourceID != nil {
		p.SourceID = *legacy.SourceID
	}
	return nil
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

// PublishPattern is a publish grant: an event type a worker may publish,
// optionally restricted to directed targets it may send to. It is the shape of
// every PublishAllow entry.
//
// A bare event-type string is accepted (via UnmarshalJSON) and is equivalent to
// a pattern with no target restriction (Target "*").
type PublishPattern struct {
	// Type matches the event type this worker may publish.
	// Supports "*" (any), "Prefix.*" (prefix), and exact match.
	Type EventType `json:"type"`
	// Target restricts directed (Send) delivery to a worker. Use "*" for any
	// target (the default, also permitting a broadcast of the type). A specific
	// target grants directed sends to that worker only — it does NOT permit
	// broadcasting the type.
	Target string `json:"target,omitempty"`
}

// UnrestrictedTarget reports whether this grant's target rule is "any target";
// only the canonical "*" value is unrestricted.
func (p PublishPattern) UnrestrictedTarget() bool {
	return p.Target == "*"
}

// MatchesType reports whether a published event type satisfies this grant's
// type rule (target-agnostic).
func (p PublishPattern) MatchesType(typ EventType) bool {
	return PatternMatches(string(p.Type), typ)
}

// BroadcastAllowed reports whether this grant permits broadcasting typ. Only an
// unrestricted grant authorizes a broadcast, since a broadcast addresses every
// matching subscriber, not one target.
func (p PublishPattern) BroadcastAllowed(typ EventType) bool {
	return p.UnrestrictedTarget() && p.MatchesType(typ)
}

// SendAllowed reports whether this grant permits a directed send of typ to the
// given target. An unrestricted grant allows any target; a targeted grant only
// that worker.
func (p PublishPattern) SendAllowed(typ EventType, target string) bool {
	if !p.MatchesType(typ) {
		return false
	}
	return p.UnrestrictedTarget() || p.Target == target
}

// NewPublishPattern builds an unrestricted publish grant (Target "*") for an
// event type.
func NewPublishPattern(typ EventType) PublishPattern {
	return PublishPattern{Type: typ, Target: "*"}
}

// UnmarshalJSON accepts a bare event-type string or a {"type","target"} object,
// so existing string-only configs and registry entries keep loading. A bare
// string grants the type with no target restriction (Target "*").
//
// The object form accepts BOTH spellings of the target field: the canonical
// "target_worker_id" (the struct tag, used by registry/config persistence) and
// the natural shorthand "target". Only the canonical tag would leave the
// shorthand silently ignored — and an empty target denies everything, so the
// mistake must not pass unnoticed.
func (p *PublishPattern) UnmarshalJSON(b []byte) error {
	var typeOnly string
	if err := json.Unmarshal(b, &typeOnly); err == nil {
		p.Type = EventType(typeOnly)
		p.Target = "*"
		return nil
	}
	type alias PublishPattern
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*p = PublishPattern(a)
	var spellings struct {
		Target         *string `json:"target"`
		TargetWorkerID *string `json:"target_worker_id"`
	}
	if err := json.Unmarshal(b, &spellings); err != nil {
		return err
	}
	switch {
	case spellings.Target != nil:
		p.Target = *spellings.Target
	case spellings.TargetWorkerID != nil:
		p.Target = *spellings.TargetWorkerID
	}
	return nil
}

// PatternMatches reports whether an event type matches a subscription pattern.
// Supports:
//   - "*"            — matches any event type
//   - exact match     — e.g. "request.completed"
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
	// RequestId pairs a request with its response: a request carries it, and
	// the response echoes it back (JSON-RPC id semantics). Empty means the
	// event is a notification — no response is expected. Distinct from ID
	// (this event's own identity) and TraceID (the reasoning trace).
	RequestId   string   `json:"request_id,omitempty"`
	TraceID     string   `json:"trace_id,omitempty"`
	SpecVersion string   `json:"specversion,omitempty"`
	DataSchema  string   `json:"dataschema,omitempty"`
	Timestamp   int64    `json:"timestamp"`
	Recipients  []string `json:"recipients,omitempty"` // populated by engine during routing

	// ExcludeWorkerID names a worker the sender does not want to receive this
	// event even if it matches the broadcast subscription. The engine skips it
	// when routing a broadcast; ignored for directed sends. Set by the source,
	// e.g. a worker excluding itself from its own presence announcement.
	ExcludeWorkerID string `json:"exclude_worker_id,omitempty"`

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
