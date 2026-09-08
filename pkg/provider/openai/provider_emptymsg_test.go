package openai

import (
	"testing"

	"github.com/niq-run/niq/core/llm"
)

// TestMessageToChatSkipsEmptyAssistant covers the fallback fix: a persisted
// assistant message that has neither content nor tool calls (the artifact of a
// meta round that collapsed to an empty assistant message) must not be shipped
// to upstream, which rejects it with a 400 and wedges every later request.
func TestMessageToChatSkipsEmptyAssistant(t *testing.T) {
	// The precise damaged shape: role assistant, zero content blocks (so the
	// snapshot round-trips to a nil slice and contentToPayload yields "").
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: nil, StopReason: "stop"},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{}},
	}

	ctx := &llm.Context{Messages: msgs}
	got := buildMessages(ctx)
	if len(got) != 0 {
		t.Fatalf("expected empty assistant messages to be dropped, got %d message(s): %+v", len(got), got)
	}
}

// TestMessageToChatKeepsMeaningfulAssistant guards against over-eager dropping:
// assistant messages that carry text, tool calls, or reasoning must still pass
// through.
func TestMessageToChatKeepsMeaningfulAssistant(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hello"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"}}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{{Type: llm.ContentThinking, Text: "think"}}},
	}

	ctx := &llm.Context{Messages: msgs}
	got := buildMessages(ctx)
	if len(got) != 3 {
		t.Fatalf("expected all 3 meaningful messages kept, got %d: %+v", len(got), got)
	}
	if got[0].Content != "hello" {
		t.Fatalf("text payload: got %#v", got[0].Content)
	}
	if len(got[1].ToolCalls) != 1 {
		t.Fatalf("tool call payload: got %+v", got[1].ToolCalls)
	}
	if got[2].ReasoningContent != "think" {
		t.Fatalf("reasoning payload: got %q", got[2].ReasoningContent)
	}
}
