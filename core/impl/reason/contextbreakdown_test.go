package reason

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/core/impl/reason/transcript"
	"github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/program"
)

// TestContextBreakdownParts verifies the round's prompt is attributed to the
// system prompt, the applied programs, the tool definitions (by provider) and
// the transcript's message kinds, and that the wire form is rescaled so the
// parts add up to the reported token counts.
func TestContextBreakdownParts(t *testing.T) {
	w := newTestWorker(&staticProvider{}, nil)
	w.programs.Applied = []program.Program{
		instrProgram("rule", strings.Repeat("rule ", 50)),
		playbookProgram("pb", "a procedure"),
	}
	w.discovered = []DiscoveredCapability{
		{Source: w.ID(), SourceType: "reason", Event: TypeMessage, Description: "send a note"},
		{Source: w.ID(), SourceType: "reason", Event: TypeRequest, Description: "request"},
		{Source: "prog-1", SourceType: ProgramWorkerType, Event: "run", Description: "run a script"},
		{Source: "mcp-1", SourceType: "mcp", Event: "fetch", Description: "fetch a url"},
		{Source: "ws-1", SourceType: "workspace", Event: "read_file", Description: "read a file"},
	}
	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: strings.Repeat("u", 400)}}},
	}})
	w.transcript.Apply(transcript.AssistantOutputPatch{Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: strings.Repeat("t", 400)},
			{Type: llm.ContentText, Text: strings.Repeat("a", 400)},
			{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "prog-1__run", ToolArguments: `{"code":"x"}`},
		},
	}})
	w.transcript.Apply(transcript.ToolPlaceholdersPatch{Calls: []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "prog-1__run"},
	}})
	w.transcript.Apply(transcript.ToolResultPatch{CallID: "c1", Name: "prog-1__run",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: strings.Repeat("r", 400)}}})

	_, _ = w.prepareReasoning()

	got := make(map[string]int, len(w.roundBreakdown))
	for _, s := range w.roundBreakdown {
		got[s.key] = s.tokens
	}
	// Every part of the prompt must be attributed.
	for _, key := range []string{
		breakdownSystem,
		breakdownProgramInstructions,
		breakdownProgramPlaybooks,
		breakdownToolsBuiltin,
		breakdownToolsProgram,
		breakdownToolsMCP,
		breakdownToolsWorkers,
		breakdownMessagesUser,
		breakdownMessagesAssistant,
		breakdownMessagesToolCalls,
		breakdownMessagesToolResults,
		breakdownMessagesThinking,
	} {
		if got[key] <= 0 {
			t.Errorf("section %q missing from breakdown: %+v", key, got)
		}
	}

	// The wire form is rescaled to the reported usage and sums exactly to it.
	meta := usageMeta(llm.Message{Usage: &llm.Usage{
		InputTokens: 1000, OutputTokens: 100, TotalTokens: 1100,
	}}, 100000, w.roundBreakdown)
	raw, ok := meta["breakdown"].([]map[string]any)
	if !ok || len(raw) == 0 {
		t.Fatalf("breakdown missing from usage meta: %+v", meta)
	}
	sum := 0
	var sawOutput bool
	for _, s := range raw {
		n, _ := s["tokens"].(int)
		sum += n
		if s["key"] == "output" {
			sawOutput = true
		}
	}
	if !sawOutput {
		t.Error("breakdown should carry the round's generated tokens as its output segment")
	}
	if sum != 1100 {
		t.Errorf("breakdown sums to %d, want the reported total 1100: %+v", sum, raw)
	}
}

// TestScaleBreakdownClampsToActual verifies estimates are rescaled to the
// provider's count even when the heuristic overshoots, and that an empty
// breakdown yields nil rather than an empty array.
func TestScaleBreakdownClampsToActual(t *testing.T) {
	sections := []contextSection{
		{key: breakdownSystem, tokens: 4000},
		{key: breakdownMessagesUser, tokens: 1000},
	}
	got := scaleBreakdown(sections, 250, 0)
	if len(got) != 2 {
		t.Fatalf("got %d sections, want 2", len(got))
	}
	sum := 0
	for _, s := range got {
		sum += s["tokens"].(int)
	}
	if sum != 250 {
		t.Fatalf("sections sum to %d, want 250", sum)
	}
	if got[0]["tokens"].(int) <= got[1]["tokens"].(int) {
		t.Fatalf("the larger estimate should stay the larger section: %+v", got)
	}

	if b := scaleBreakdown(nil, 100, 10); b != nil {
		t.Fatalf("empty breakdown should be nil, got %+v", b)
	}
}
