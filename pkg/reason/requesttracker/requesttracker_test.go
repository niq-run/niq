package requesttracker

import (
	"testing"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
)

func tcBlock(id, name string) llm.ContentBlock {
	return llm.ContentBlock{Type: llm.ContentToolCall, ToolCallID: id, ToolName: name}
}

func resultEvent(typ event.EventType, callID string) event.Event {
	return event.New(typ, "workspace", map[string]any{"call_id": callID})
}

// TestRequestTrackerAddPending verifies Add records calls as Pending and
// Resolved is false while any call is pending.
func TestRequestTrackerAddPending(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash"), tcBlock("b", "read")})
	if m.Resolved() {
		t.Fatal("tracker should not be resolved with pending calls")
	}
}

// TestRequestTrackerHandleResponse verifies a result event removes a Pending
// call, and Resolved becomes true once all are resolved.
func TestRequestTrackerHandleResponse(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash"), tcBlock("b", "read")})

	if !m.HandleResponse(resultEvent(event.TypeToolCompleted, "a")) {
		t.Fatal("handleResponse should match call a")
	}
	if m.Resolved() {
		t.Fatal("still pending call b")
	}
	if !m.HandleResponse(resultEvent(event.TypeToolFailed, "b")) {
		t.Fatal("handleResponse should match call b")
	}
	if !m.Resolved() {
		t.Fatal("all calls resolved")
	}
}

// TestRequestTrackerHandleResponseUnknown verifies a result event for an
// untracked call ID is ignored.
func TestRequestTrackerHandleResponseUnknown(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash")})
	if m.HandleResponse(resultEvent(event.TypeToolCompleted, "nope")) {
		t.Fatal("unknown call_id must not match")
	}
	if m.Resolved() {
		t.Fatal("call a still pending")
	}
}

// TestRequestTrackerParkAll verifies parkAll marks only still-Pending calls as
// Parked (already-resolved calls are gone), and returns the parked calls.
func TestRequestTrackerParkAll(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash"), tcBlock("b", "read")})
	m.HandleResponse(resultEvent(event.TypeToolCompleted, "a")) // a resolved

	parked := m.ParkAll(PreemptCauseTimeout)
	if len(parked) != 1 || parked[0].CallID != "b" {
		t.Fatalf("parkAll should park only pending call b, got %+v", parked)
	}
	if parked[0].Status != RequestParked || parked[0].ParkCause != PreemptCauseTimeout {
		t.Fatalf("parked call should be Parked with timeout cause, got %+v", parked[0])
	}
	if !m.Resolved() {
		t.Fatal("after parkAll, no pending calls remain (Resolved should be true)")
	}
}

// TestRequestTrackerResolveLate verifies a late result on a Parked call is
// matched and removed, and a late result on an untracked call is ignored.
func TestRequestTrackerResolveLate(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash")})
	m.ParkAll(PreemptCauseTimeout)

	if got := m.ResolveLate(resultEvent(event.TypeToolCompleted, "a")); got == nil {
		t.Fatal("late result should match parked call a")
	}
	if m.ResolveLate(resultEvent(event.TypeToolCompleted, "z")) != nil {
		t.Fatal("late result for untracked call should be ignored")
	}
}

// TestRequestTrackerResolvedEmpty verifies a fresh tracker is considered
// resolved (no pending calls to await).
func TestRequestTrackerResolvedEmpty(t *testing.T) {
	m := NewRequestTracker()
	if !m.Resolved() {
		t.Fatal("empty tracker should be resolved")
	}
}
