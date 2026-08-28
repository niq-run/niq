// tool call lifecycle tracking.
//
// ToolCallTracker: track pending calls, match tool result events,
// park / resolve / recall. It manages its own pending map only — the
// caller is responsible for publishing tool.request / cancel events
// to the bus and for building outcome messages.
//
// Status only tracks the state of calls still present in the map:
//
// Add          → Pending (reasoner is waiting)
// parkAll      → Parked  (stopped waiting, kept for late results)
// tool result  → removed (outcome handled by the caller from the event)
// late result  → matched via resolveLate, removed, contextualized
//
// Terminal outcomes (completed / failed / rejected) are not tracked here —
// they are message-level concepts built by the caller from the event.
package reason

import (
	"sync"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// ToolReqStatus tracks the lifecycle stage of a tool call while it is tracked.
type ToolReqStatus string

const (
	ToolPending ToolReqStatus = "pending" // active: reasoner is waiting on this call
	ToolParked  ToolReqStatus = "parked"  // no longer awaited; may still return late
)

// PreemptCause records why a call was parked, so late-result messages can be
// framed with the correct context. Also used to track why a reasoning round
// was interrupted (abort/input preemption) or a new round was triggered
// (reminder/timeout).
type PreemptCause string

const (
	PreemptCauseInput    PreemptCause = "input"    // a new user input preempted the wait
	PreemptCauseTimeout  PreemptCause = "timeout"  // the batch timeout fired
	PreemptCauseAbort    PreemptCause = "abort"    // an abort signal ended the call
	PreemptCauseReminder PreemptCause = "reminder" // an elapse reminder timer fired
)

// ToolCall tracks a single tool call through its event-bus lifecycle.
type ToolCall struct {
	CallID    string
	Name      string
	TargetID  string // worker the tool.request was sent to (used for recall)
	Status    ToolReqStatus
	ParkCause PreemptCause // meaningful when Status == ToolParked
}

// ToolCallTracker manages the lifecycle of tool calls in the pending map.
// It tracks Pending/Parked calls; the caller publishes tool.request / cancel
// events to the bus and resolves tool result events via handleResponse/resolveLate.
type ToolCallTracker struct {
	mu      sync.Mutex
	pending map[string]*ToolCall // keyed by callID
}

// NewToolCallTracker creates an empty ToolCallTracker.
func NewToolCallTracker() *ToolCallTracker {
	return &ToolCallTracker{
		pending: make(map[string]*ToolCall),
	}
}

// Add begins tracking tool calls as Pending.
// targetID is the worker the calls were/will be addressed to (recorded for
// recall). The caller is responsible for publishing the corresponding
// tool.request events to each target.
func (m *ToolCallTracker) Add(targetID string, toolCalls []llm.ContentBlock) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, tc := range toolCalls {
		if tc.Type != llm.ContentToolCall {
			continue
		}
		m.pending[tc.ToolCallID] = &ToolCall{
			CallID:   tc.ToolCallID,
			Name:     tc.ToolName,
			TargetID: targetID,
			Status:   ToolPending,
		}
	}
}

// HandleResponse reports whether a Pending call matched the tool result event
// and was removed from the tracker. It does not interpret the event — the
// outcome (result / fail / reject reason) is read by the caller when building
// the message.
func (m *ToolCallTracker) HandleResponse(evt event.Event) bool {
	callID, _ := evt.Payload["call_id"].(string)
	if callID == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok || run.Status != ToolPending {
		return false
	}
	delete(m.pending, callID)
	return true
}

// ParkAll marks every still-Pending call as Parked with the given cause and
// returns them. Parked calls are kept in the tracker so late results can be
// matched and given context. Already-parked or resolved calls are untouched.
func (m *ToolCallTracker) ParkAll(cause PreemptCause) []*ToolCall {
	m.mu.Lock()
	defer m.mu.Unlock()

	var tcs []*ToolCall
	for _, call := range m.pending {
		if call.Status != ToolPending {
			continue
		}
		call.Status = ToolParked
		call.ParkCause = cause
		tcs = append(tcs, call)
	}
	return tcs
}

// ResolveLate matches a tool result event against a Parked call — a late
// result. The call is removed from the tracker and returned so the caller can
// build a contextualized late-result message. Returns nil if no Parked call
// matches.
func (m *ToolCallTracker) ResolveLate(evt event.Event) *ToolCall {
	callID, _ := evt.Payload["call_id"].(string)
	if callID == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	run, ok := m.pending[callID]
	if !ok || run.Status != ToolParked {
		return nil
	}
	delete(m.pending, callID)
	return run
}

// Resolved reports whether every tracked call has reached a terminal or
// parked state (i.e. no call is still being awaited).
func (m *ToolCallTracker) Resolved() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, call := range m.pending {
		if call.Status == ToolPending {
			return false
		}
	}
	return true
}
