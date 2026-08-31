// Outgoing tool.request lifecycle tracking.
//
// A tool call is two things under two names: the LLM sees a tool call in the
// transcript, while this worker puts a tool.request on the bus and waits for
// its result. This package owns the bus-side half — RequestTracker follows the
// tool.request events this worker has issued until their results come back.
// It manages its own pending map only; the caller is responsible for
// publishing tool.request / cancel events to the bus and for building outcome
// messages.
//
// Status only tracks the state of calls still present in the map:
//
// Add          → Pending (reasoner is waiting)
// parkAll      → Parked  (stopped waiting, kept for late results)
// tool result  → removed (outcome handled by the caller from the event)
// late result  → matched via ResolveLate, removed, contextualized
//
// Terminal outcomes (completed / failed / rejected) are not tracked here —
// they are message-level concepts built by the caller from the event.
package requesttracker

import (
	"sync"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
)

// PreemptCause records why a wait was preempted. It has two consumers:
//
//   - why an issued tool.request was parked (so a late result can be framed
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
)

// RequestStatus tracks the lifecycle stage of an issued request while it is
// tracked.
type RequestStatus string

const (
	RequestPending RequestStatus = "pending" // active: reasoner is waiting on this request
	RequestParked  RequestStatus = "parked"  // no longer awaited; may still return late
)

// TrackedRequest is one issued tool.request, from dispatch until its result
// arrives (or it is parked and later matched as a late result).
type TrackedRequest struct {
	CallID    string
	Name      string
	TargetID  string // worker the tool.request was sent to (used for recall)
	Status    RequestStatus
	ParkCause PreemptCause // meaningful when Status == RequestParked
}

// RequestTracker manages the lifecycle of the tool.request events this worker
// has issued, in the pending map. It tracks Pending/Parked requests; the
// caller publishes tool.request / cancel events to the bus and resolves tool
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
// tool.request events to each target.
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

// HandleResponse reports whether a Pending request matched the tool result
// event and was removed from the tracker. It does not interpret the event —
// the outcome (result / fail / reject reason) is read by the caller when
// building the message.
func (m *RequestTracker) HandleResponse(evt event.Event) bool {
	callID := evt.RequestId
	if callID == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok || run.Status != RequestPending {
		return false
	}
	delete(m.pending, callID)
	return true
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
