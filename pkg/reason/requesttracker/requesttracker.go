// Outgoing request lifecycle tracking.
//
// A tool call is two things under two names: the LLM sees a tool call in the
// transcript, while this worker invokes the capability on the bus under the
// extension's own event type and waits for the request.completed / request.failed /
// request.rejected result that echoes the call's RequestId. This package owns
// the bus-side half — RequestTracker follows the requests this worker has
// issued until their results come back. It manages its own pending map only;
// the caller is responsible for publishing the invocation / request.cancel
// events to the bus and for building outcome messages.
//
// Status only tracks the state of calls still present in the map:
//
// Add          → Pending (reasoner is waiting)
// parkAll      → Parked  (stopped waiting, kept for late results)
// result       → removed (outcome handled by the caller from the event)
// late result  → matched via ResolveLate, removed, contextualized
//
// Terminal outcomes (completed / failed / rejected) are not tracked here —
// they are message-level concepts built by the caller from the event.
//
// State/Restore carry the whole map across a worker restart. Restore is
// faithful (a Pending call comes back Pending); deciding what a restored wait
// still means is the caller's policy, not this package's.
package requesttracker

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
)

// PreemptCause records why a wait was preempted. It has two consumers:
//
//   - why an issued request was parked (so a late result can be framed
//     with the correct context), and
//   - why a reasoning round was interrupted (abort / input preemption) or a
//     new one triggered (reminder / timeout).
//
// The four values mirror the four preemption sources; pkg/reason's handlers
// produce them, and the tracker carries them on parked requests so late
// results can be framed.
type PreemptCause string

const (
	PreemptCauseInput    PreemptCause = "input"    // a new user input preempted the wait
	PreemptCauseTimeout  PreemptCause = "timeout"  // the batch timeout fired
	PreemptCauseAbort    PreemptCause = "abort"    // an abort signal ended the call
	PreemptCauseReminder PreemptCause = "reminder" // an elapse reminder timer fired
	PreemptCauseRestart  PreemptCause = "restart"  // the worker restarted; the wait did not survive it
)

// RequestStatus tracks the lifecycle stage of an issued request while it is
// tracked.
type RequestStatus string

const (
	RequestPending RequestStatus = "pending" // active: reasoner is waiting on this request
	RequestParked  RequestStatus = "parked"  // no longer awaited; may still return late
)

// TrackedRequest is one issued request, from dispatch until its result
// arrives (or it is parked and later matched as a late result).
type TrackedRequest struct {
	CallID    string
	Name      string
	TargetID  string // worker the invocation was sent to (used for recall)
	Status    RequestStatus
	ParkCause PreemptCause // meaningful when Status == RequestParked
}

// RequestTracker manages the lifecycle of the requests this worker has
// issued, in the pending map. It tracks Pending/Parked requests; the caller
// publishes the invocation / request.cancel events to the bus and resolves
// result events via HandleResponse / ResolveLate.
type RequestTracker struct {
	mu      sync.Mutex
	pending map[string]*TrackedRequest // keyed by callID
}

// NewRequestTracker creates an empty RequestTracker.
func NewRequestTracker() *RequestTracker {
	return &RequestTracker{
		pending: make(map[string]*TrackedRequest),
	}
}

// Add begins tracking tool calls as Pending requests.
// targetID is the worker the calls were/will be addressed to (recorded for
// recall). The caller is responsible for publishing the corresponding
// invocation events to each target.
func (m *RequestTracker) Add(targetID string, toolCalls []llm.ContentBlock) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tc := range toolCalls {
		if tc.Type != llm.ContentToolCall {
			continue
		}
		m.pending[tc.ToolCallID] = &TrackedRequest{
			CallID:   tc.ToolCallID,
			Name:     tc.ToolName,
			TargetID: targetID,
			Status:   RequestPending,
		}
	}
}

// HandleResponse matches a tool result event against a Pending request and
// removes it from the tracker. It returns the matched request (carrying the
// tool name, which the result event no longer self-describes) or nil when the
// event does not resolve a pending call. It does not interpret the event —
// the outcome (result / fail / reject reason) is read by the caller when
// building the message.
func (m *RequestTracker) HandleResponse(evt event.Event) *TrackedRequest {
	callID := evt.RequestId
	if callID == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok || run.Status != RequestPending {
		return nil
	}
	delete(m.pending, callID)
	return run
}

// ParkAll marks every still-Pending request as Parked with the given cause and
// returns them. Parked requests are kept in the tracker so late results can be
// matched and given context. Already-parked or resolved requests are untouched.
func (m *RequestTracker) ParkAll(cause PreemptCause) []*TrackedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	var tcs []*TrackedRequest
	for _, call := range m.pending {
		if call.Status != RequestPending {
			continue
		}
		call.Status = RequestParked
		call.ParkCause = cause
		tcs = append(tcs, call)
	}
	return tcs
}

// ResolveLate matches a tool result event against a Parked request — a late
// result. The request is removed from the tracker and returned so the caller
// can build a contextualized late-result message. Returns nil if no Parked
// request matches.
func (m *RequestTracker) ResolveLate(evt event.Event) *TrackedRequest {
	callID := evt.RequestId
	if callID == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok || run.Status != RequestParked {
		return nil
	}
	delete(m.pending, callID)
	return run
}

// trackedRequestState is the persisted form of one tracked request. The field
// set may only grow: older blobs stay readable.
type trackedRequestState struct {
	CallID    string        `json:"call_id"`
	Name      string        `json:"name"`
	TargetID  string        `json:"target_id"`
	Status    RequestStatus `json:"status"`
	ParkCause PreemptCause  `json:"park_cause,omitempty"`
}

// State serializes every request still in the map. An empty tracker yields a
// nil blob (nothing worth persisting), so a snapshot taken between calls does
// not grow.
func (m *RequestTracker) State() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.pending) == 0 {
		return nil, nil
	}
	reqs := make([]trackedRequestState, 0, len(m.pending))
	for _, call := range m.pending {
		reqs = append(reqs, trackedRequestState{
			CallID:    call.CallID,
			Name:      call.Name,
			TargetID:  call.TargetID,
			Status:    call.Status,
			ParkCause: call.ParkCause,
		})
	}
	return json.Marshal(reqs)
}

// Restore rehydrates the map from a State blob, replacing whatever it held. An
// empty blob is a no-op (it is what a tracker with nothing to persist writes).
// Entries that could never be matched are dropped: no call id, or a status
// that is neither Pending nor Parked (HandleResponse and ResolveLate would both
// skip it, leaving it in the map unresolved forever).
func (m *RequestTracker) Restore(state []byte) error {
	if len(state) == 0 {
		return nil
	}
	var reqs []trackedRequestState
	if err := json.Unmarshal(state, &reqs); err != nil {
		return fmt.Errorf("requesttracker restore: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.pending = make(map[string]*TrackedRequest, len(reqs))
	for _, r := range reqs {
		if r.CallID == "" || (r.Status != RequestPending && r.Status != RequestParked) {
			continue
		}
		m.pending[r.CallID] = &TrackedRequest{
			CallID:    r.CallID,
			Name:      r.Name,
			TargetID:  r.TargetID,
			Status:    r.Status,
			ParkCause: r.ParkCause,
		}
	}
	return nil
}

// Resolved reports whether every tracked request has reached a terminal or
// parked state (i.e. no request is still being awaited).
func (m *RequestTracker) Resolved() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, call := range m.pending {
		if call.Status == RequestPending {
			return false
		}
	}
	return true
}
