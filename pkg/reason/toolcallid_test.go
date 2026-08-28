package reason

import (
	"testing"

	llm "github.com/54c1/niq/core/llm"
)

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
