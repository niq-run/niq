package reason

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

// TestToolCallTrackerAddPending verifies Add records calls as Pending and
// Resolved is false while any call is pending.
func TestToolCallTrackerAddPending(t *testing.T) {
	m := NewToolCallTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash"), tcBlock("b", "read")})
	if m.Resolved() {
		t.Fatal("tracker should not be resolved with pending calls")
	}
}

// TestToolCallTrackerHandleResponse verifies a result event removes a Pending
// call, and Resolved becomes true once all are resolved.
func TestToolCallTrackerHandleResponse(t *testing.T) {
	m := NewToolCallTracker()
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

// TestToolCallTrackerHandleResponseUnknown verifies a result event for an
// untracked call ID is ignored.
func TestToolCallTrackerHandleResponseUnknown(t *testing.T) {
	m := NewToolCallTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash")})
	if m.HandleResponse(resultEvent(event.TypeToolCompleted, "nope")) {
		t.Fatal("unknown call_id must not match")
	}
	if m.Resolved() {
		t.Fatal("call a still pending")
	}
}

// TestToolCallTrackerParkAll verifies parkAll marks only still-Pending calls as
// Parked (already-resolved calls are gone), and returns the parked calls.
func TestToolCallTrackerParkAll(t *testing.T) {
	m := NewToolCallTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash"), tcBlock("b", "read")})
	m.HandleResponse(resultEvent(event.TypeToolCompleted, "a")) // a resolved

	parked := m.ParkAll(PreemptCauseTimeout)
	if len(parked) != 1 || parked[0].CallID != "b" {
		t.Fatalf("parkAll should park only pending call b, got %+v", parked)
	}
	if parked[0].Status != ToolParked || parked[0].ParkCause != PreemptCauseTimeout {
		t.Fatalf("parked call should be Parked with timeout cause, got %+v", parked[0])
	}
	if !m.Resolved() {
		t.Fatal("after parkAll, no pending calls remain (Resolved should be true)")
	}
}

// TestToolCallTrackerResolveLate verifies a late result on a Parked call is
// matched and removed, and a late result on an untracked call is ignored.
func TestToolCallTrackerResolveLate(t *testing.T) {
	m := NewToolCallTracker()
	m.Add("workspace", []llm.ContentBlock{tcBlock("a", "bash")})
	m.ParkAll(PreemptCauseTimeout)

	if got := m.ResolveLate(resultEvent(event.TypeToolCompleted, "a")); got == nil {
		t.Fatal("late result should match parked call a")
	}
	if m.ResolveLate(resultEvent(event.TypeToolCompleted, "z")) != nil {
		t.Fatal("late result for untracked call should be ignored")
	}
}

// TestToolCallTrackerResolvedEmpty verifies a fresh tracker is considered
// resolved (no pending calls to await).
func TestToolCallTrackerResolvedEmpty(t *testing.T) {
	m := NewToolCallTracker()
	if !m.Resolved() {
		t.Fatal("empty tracker should be resolved")
	}
}

// TestEnsureToolCallIDsSynthesizesMissing verifies the transcript invariant:
// every tool call entering the transcript carries a non-empty id, even when the
// model omitted it — otherwise the OpenAI-format pair (assistant tool_call +
// tool_result) is invalid and, because the transcript is persisted,
// unrecoverable.
func TestEnsureToolCallIDsSynthesizesMissing(t *testing.T) {
	w := newTestWorker(nil, nil)
	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.ContentToolCall, ToolName: "bash", ToolArguments: "{}"},
			{Type: llm.ContentToolCall, ToolCallID: "model-id", ToolName: "ls", ToolArguments: "{}"},
			{Type: llm.ContentToolCall, ToolName: "read", ToolArguments: "{}"},
		},
	}
	w.ensureToolCallIDs(msg)

	// The model-supplied id is preserved.
	if msg.Content[1].ToolCallID != "model-id" {
		t.Fatalf("expected model id preserved, got %q", msg.Content[1].ToolCallID)
	}
	if msg.Content[0].ToolCallID == "" || msg.Content[2].ToolCallID == "" {
		t.Fatalf("expected synthesized ids, got %q and %q", msg.Content[0].ToolCallID, msg.Content[2].ToolCallID)
	}
	if msg.Content[0].ToolCallID == msg.Content[2].ToolCallID {
		t.Fatalf("expected distinct ids, both %q", msg.Content[0].ToolCallID)
	}
}

// TestEnsureToolCallIDsUniqueAcrossRounds verifies ids do not repeat between
// calls: the tracker keeps pending calls across rounds, so a repeated id would
// mispair a result against the wrong call.
func TestEnsureToolCallIDsUniqueAcrossRounds(t *testing.T) {
	w := newTestWorker(nil, nil)
	var ids []string
	for i := 0; i < 3; i++ {
		msg := llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolName: "bash"}},
		}
		w.ensureToolCallIDs(msg)
		ids = append(ids, msg.Content[0].ToolCallID)
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if ids[i] == ids[j] {
				t.Fatalf("id %q repeated across rounds %d and %d", ids[i], i, j)
			}
		}
	}
}
