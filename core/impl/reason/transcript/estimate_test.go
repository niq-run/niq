package transcript

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/llm"
)

// TestEstimateByKindPartitionsTheEstimate verifies the kind breakdown splits a
// transcript into the kinds a caller can report on, and that the split is a
// partition of the same estimate compaction budgets use — the two must never
// drift apart, or a UI breakdown would contradict the worker's own budget math.
func TestEstimateByKindPartitionsTheEstimate(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: strings.Repeat("u", 400)},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: strings.Repeat("t", 400)},
			{Type: llm.ContentText, Text: strings.Repeat("a", 400)},
		}},
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{
			{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "run", ToolArguments: strings.Repeat("x", 400)},
		}},
		{Role: llm.RoleToolResult, ToolCallID: "c1", ToolName: "run", Content: []llm.ContentBlock{
			{Type: llm.ContentText, Text: strings.Repeat("r", 400)},
		}},
	}

	got := EstimateByKind(msgs)
	for _, kind := range []Kind{KindInput, KindAssistant, KindThinking, KindToolCall, KindToolResult} {
		if got[kind] <= 0 {
			t.Errorf("kind %q should be estimated, got %d (%+v)", kind, got[kind], got)
		}
	}

	var want int
	for _, m := range msgs {
		want += estimateMessageTokens(m)
	}
	sum := 0
	for _, n := range got {
		sum += n
	}
	if sum != want {
		t.Fatalf("kinds sum to %d, estimateMessageTokens reports %d — the partition drifted", sum, want)
	}
}

// TestEstimateText verifies the text estimator treats an absent section as free
// (so callers can size optional prompt sections as a delta) and grows with size.
func TestEstimateText(t *testing.T) {
	if n := EstimateText(""); n != 0 {
		t.Fatalf("empty text should cost 0, got %d", n)
	}
	if EstimateText("hello") >= EstimateText(strings.Repeat("hello", 10)) {
		t.Fatal("estimate should grow with text length")
	}
}
