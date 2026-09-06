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
	evt := event.New(typ, "workspace", nil)
	evt.RequestId = callID
	return evt
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

	if tr := m.HandleResponse(resultEvent(event.TypeRequestCompleted, "a")); tr == nil {
		t.Fatal("handleResponse should match call a")
	} else if tr.Name != "bash" {
		t.Fatalf("matched request name = %q, want bash", tr.Name)
	}
	if m.Resolved() {
		t.Fatal("still pending call b")
	}
	if tr := m.HandleResponse(resultEvent(event.TypeRequestFailed, "b")); tr == nil {
		t.Fatal("handleResponse should match call b")
	} else if tr.Name != "read" {
		t.Fatalf("matched request name = %q, want read", tr.Name)
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
	if m.HandleResponse(resultEvent(event.TypeRequestCompleted, "nope")) != nil {
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
	m.HandleResponse(resultEvent(event.TypeRequestCompleted, "a")) // a resolved

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

	if got := m.ResolveLate(resultEvent(event.TypeRequestCompleted, "a")); got == nil {
		t.Fatal("late result should match parked call a")
	}
	if m.ResolveLate(resultEvent(event.TypeRequestCompleted, "z")) != nil {
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

// TestRequestTrackerStateEmpty verifies an empty tracker persists nothing and
// an empty blob restores as a no-op.
func TestRequestTrackerStateEmpty(t *testing.T) {
	m := NewRequestTracker()
	blob, err := m.State()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	if blob != nil {
		t.Fatalf("empty tracker should persist nil, got %s", blob)
	}

	fresh := NewRequestTracker()
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore(nil): %v", err)
	}
	if !fresh.Resolved() {
		t.Fatal("restore of an empty blob should leave the tracker empty and resolved")
	}
}

// TestRequestTrackerStateRoundTrip verifies State/Restore carries every field
// of tracked requests across, and that restored requests still match results
// (Pending via HandleResponse, Parked via ResolveLate).
func TestRequestTrackerStateRoundTrip(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash"), tcBlock("b", "read")})
	m.Add("timer", []llm.ContentBlock{tcBlock("c", "sleep")})
	m.HandleResponse(resultEvent(event.TypeRequestCompleted, "a")) // a resolved, gone
	m.ParkAll(PreemptCauseTimeout)                                 // b and c parked

	blob, err := m.State()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	fresh := NewRequestTracker()
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}

	// Both restored calls are Parked (ParkAll ran before the snapshot) and both
	// still match their results.
	if tr := fresh.ResolveLate(resultEvent(event.TypeRequestFailed, "c")); tr == nil {
		t.Fatal("restored parked call c should match a late result")
	} else if tr.Name != "sleep" || tr.TargetID != "timer" || tr.ParkCause != PreemptCauseTimeout {
		t.Fatalf("restored call c = %+v, want name sleep target timer cause timeout", tr)
	}
	if tr := fresh.ResolveLate(resultEvent(event.TypeRequestCompleted, "b")); tr == nil {
		t.Fatal("restored parked call b should match a late result")
	} else if tr.Name != "read" || tr.TargetID != "workspace" || tr.ParkCause != PreemptCauseTimeout {
		t.Fatalf("restored parked call b = %+v, want name read target workspace cause timeout", tr)
	}
	if !fresh.Resolved() {
		t.Fatal("all restored calls should be matched by now")
	}
}

// TestRequestTrackerStateRoundTripPending verifies a snapshot taken while a
// call is still Pending restores it as Pending, and it then resolves normally.
func TestRequestTrackerStateRoundTripPending(t *testing.T) {
	m := NewRequestTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash")})

	blob, err := m.State()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	fresh := NewRequestTracker()
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if fresh.Resolved() {
		t.Fatal("restored pending call should not count as resolved")
	}
	if tr := fresh.HandleResponse(resultEvent(event.TypeRequestCompleted, "a")); tr == nil {
		t.Fatal("restored pending call a should match a result event")
	} else if tr.Name != "bash" || tr.TargetID != "workspace" {
		t.Fatalf("restored call a = %+v, want name bash target workspace", tr)
	}
	if !fresh.Resolved() {
		t.Fatal("call a should be resolved after matching")
	}
}

// TestRequestTrackerRestoreSkipsUnmatchable verifies entries without a call id
// are dropped on restore: no result can ever match them, and they would sit in
// the map unresolved forever.
func TestRequestTrackerRestoreSkipsUnmatchable(t *testing.T) {
	m := NewRequestTracker()
	blob := []byte(`[{"call_id":"","name":"bash"},{"call_id":"a","name":"read","status":"pending"},{"call_id":"z","name":"x","status":"weird"}]`)
	if err := m.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if tr := m.HandleResponse(resultEvent(event.TypeRequestCompleted, "a")); tr == nil {
		t.Fatal("call a should have been restored")
	}
	if !m.Resolved() {
		t.Fatal("the empty call_id entry should have been dropped")
	}
}

// TestRequestTrackerRestoreCorrupt verifies a corrupt blob reports an error
// instead of silently producing a half-built map.
func TestRequestTrackerRestoreCorrupt(t *testing.T) {
	m := NewRequestTracker()
	if err := m.Restore([]byte("not json")); err == nil {
		t.Fatal("corrupt blob should fail restore")
	}
}
