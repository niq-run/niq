package iface

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// Approvals are the human-in-the-loop part of an interface worker, so the
// mechanism lives here rather than in each app: every outward-facing surface
// (a web UI, a terminal, a menubar notifier) parks the same requests, lists the
// same inbox, and answers with the same decision.
//
// It is opt-in (Config.Approvals) because serving approvals is a DECLARATION on
// the bus: the worker must advertise approval.request in its watch contract and
// subscribe to it. That declaration belongs to the worker — an injected backend
// cannot make it, because the watch entries come from the worker's own extension
// registry.

// ApprovalDecision is the human's verdict on one approval request.
type ApprovalDecision struct {
	Approved  bool   `json:"approved"`
	Note      string `json:"note,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// ApprovalEntry is one approval request the worker tracks for its surface.
type ApprovalEntry struct {
	EventID   string `json:"id"`             // approval.request event id
	RequestID string `json:"request_id"`     // correlation key (= its RequestId)
	WorkerID  string `json:"worker_id"`      // requester
	Action    string `json:"action"`         // e.g. "mount.add"
	Tool      string `json:"tool,omitempty"` // tool that raised the request
	Path      string `json:"path,omitempty"` // boundary-expansion target
	TraceID   string `json:"trace_id,omitempty"`
	Timestamp int64  `json:"timestamp"`
	// Payload is the request's full payload, carried through for a surface to
	// render generically — approval kinds differ, and a UI should not need a
	// schema per kind.
	Payload  map[string]any    `json:"payload,omitempty"`
	Decision *ApprovalDecision `json:"decision,omitempty"`
}

// maxDecidedApprovals bounds the decided history kept in memory.
const maxDecidedApprovals = 100

// Inbox is the approval surface handed to a backend when Config.Approvals is on.
// It is optional on the handler: a backend asserts it
//
//	if inbox, ok := h.(iface.Inbox); ok { ... }
//
// A backend that does not serve a human simply never asserts it.
type Inbox interface {
	// Pending returns the undecided entries, oldest first.
	Pending() []ApprovalEntry
	// Decided returns the decided entries, newest first.
	Decided() []ApprovalEntry
	// Decide records the verdict and publishes approval.decision to the
	// requester, echoing its request and trace ids. It is the ONLY way a request
	// is answered: approval.request has no request.completed reply.
	Decide(ctx context.Context, requestID string, approved bool, note string) error
	// OnChange registers a callback fired when the inbox changes (a request
	// arrived, a verdict was recorded), so a surface can refresh a badge or a
	// panel without polling. It returns a cancel func.
	OnChange(fn func()) func()
}

// approvals is the inbox implementation. One per worker, created only when the
// feature is on.
type approvals struct {
	h Handler // the worker's own handler, used to publish decisions

	mu      sync.Mutex
	pending []*ApprovalEntry
	decided []*ApprovalEntry
	onFoo   []func()
}

func newApprovals(h Handler) *approvals { return &approvals{h: h} }

// registerApprovalRequest declares the capability: peers, the directory roster
// and the worker's own watch contract learn that this worker serves human
// approvals, with a schema for the envelope.
func (a *approvals) registerOn(w *Worker) {
	w.Register(baseworker.Extension{
		Event: event.TypeApprovalRequest,
		Description: "Ask the human (via this interface worker) to approve an action that needs a wider boundary " +
			"or a manual go-ahead. The request is parked in the human's inbox; the answer is approval.decision " +
			"carrying the same request id. There is no request.completed: the decision IS the reply. The payload " +
			"is an envelope — action names what is being approved, everything else is the requester's own context.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"description": "What the human is being asked to approve (e.g. mount.add). It labels the inbox entry.",
				},
			},
			// Everything beyond action is action-specific context the human needs to
			// decide; it is carried through untouched and rendered as-is.
			"additionalProperties": true,
		},
	}, func(evt event.Event) {
		// No reply: the decision answered later is the reply, and answering twice
		// would break the requester's correlation.
		a.record(evt)
	})
}

// newApprovalEntry projects a request event into an inbox entry.
func newApprovalEntry(evt event.Event) *ApprovalEntry {
	entry := &ApprovalEntry{
		EventID:   evt.ID,
		RequestID: evt.RequestId,
		WorkerID:  evt.WorkerId,
		TraceID:   evt.TraceID,
		Timestamp: evt.Timestamp,
		Payload:   evt.Payload,
	}
	if evt.Payload != nil {
		entry.Action, _ = evt.Payload["action"].(string)
		entry.Tool, _ = evt.Payload["tool"].(string)
		entry.Path, _ = evt.Payload["path"].(string)
	}
	return entry
}

// record parks one request.
func (a *approvals) record(evt event.Event) {
	entry := newApprovalEntry(evt)
	a.mu.Lock()
	a.pending = append(a.pending, entry)
	a.mu.Unlock()
	log.Printf("[iface %s] approval requested by %s: %s %s", a.h.ID(), entry.WorkerID, entry.Tool, entry.Path)
	a.changed()
}

// Pending implements Inbox.
func (a *approvals) Pending() []ApprovalEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ApprovalEntry, 0, len(a.pending))
	for _, e := range a.pending {
		out = append(out, *e)
	}
	return out
}

// Decided implements Inbox.
func (a *approvals) Decided() []ApprovalEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ApprovalEntry, 0, len(a.decided))
	for _, e := range a.decided {
		out = append(out, *e)
	}
	return out
}

// Decide implements Inbox.
func (a *approvals) Decide(ctx context.Context, requestID string, approved bool, note string) error {
	a.mu.Lock()
	idx := -1
	for i, e := range a.pending {
		if e.RequestID == requestID {
			idx = i
			break
		}
	}
	if idx < 0 {
		a.mu.Unlock()
		return fmt.Errorf("no pending approval with request id %s", requestID)
	}
	entry := a.pending[idx]
	a.pending = append(a.pending[:idx], a.pending[idx+1:]...)
	entry.Decision = &ApprovalDecision{Approved: approved, Note: note, Timestamp: time.Now().Unix()}
	a.decided = append([]*ApprovalEntry{entry}, a.decided...)
	if len(a.decided) > maxDecidedApprovals {
		a.decided = a.decided[:maxDecidedApprovals]
	}
	a.mu.Unlock()

	evt := event.New(event.TypeApprovalDecision, a.h.ID(), map[string]any{
		"approved": approved,
		"note":     note,
	})
	evt.RequestId = entry.RequestID
	evt.TraceID = entry.TraceID
	if err := a.h.Send(ctx, evt, entry.WorkerID); err != nil {
		return fmt.Errorf("send decision to %s: %w", entry.WorkerID, err)
	}
	log.Printf("[iface %s] approval %v sent to %s (request %s)", a.h.ID(), approved, entry.WorkerID, entry.RequestID)
	a.changed()
	return nil
}

// OnChange implements Inbox.
func (a *approvals) OnChange(fn func()) func() {
	if fn == nil {
		return func() {}
	}
	a.mu.Lock()
	a.onFoo = append(a.onFoo, fn)
	idx := len(a.onFoo) - 1
	a.mu.Unlock()
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if idx < len(a.onFoo) {
			a.onFoo[idx] = nil
		}
	}
}

// changed fires the change callbacks outside the lock, so a surface may read the
// inbox from its callback.
func (a *approvals) changed() {
	a.mu.Lock()
	fns := append([]func(){}, a.onFoo...)
	a.mu.Unlock()
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[iface %s] approval change callback panic: %v", a.h.ID(), r)
				}
			}()
			fn()
		}()
	}
}

// approvalsState is the inbox's share of the worker snapshot: what was parked and
// what was already settled.
type approvalsState struct {
	Pending []*ApprovalEntry `json:"pending"`
	Decided []*ApprovalEntry `json:"decided"`
}

// snapshot captures the inbox.
func (a *approvals) snapshot() *approvalsState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &approvalsState{Pending: a.pending, Decided: a.decided}
}

// restore rehydrates the inbox from a snapshot.
func (a *approvals) restore(state *approvalsState) {
	if state == nil {
		return
	}
	a.mu.Lock()
	a.pending, a.decided = state.Pending, state.Decided
	a.mu.Unlock()
	log.Printf("[iface %s] restored %d pending / %d decided approval(s)", a.h.ID(), len(state.Pending), len(state.Decided))
}
