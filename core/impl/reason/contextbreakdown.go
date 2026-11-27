// Context breakdown: what a reasoning round's prompt is made of.
//
// The provider reports one input-token number for the whole request, which
// tells the user how full the window is but not WHY. This file attributes the
// prompt to its parts — the system prompt's base template, the applied programs
// (instructions / playbooks), the tool definitions (by the worker that offers
// them) and the transcript (conversation, tool calls, tool results, thinking) —
// and ships the result on reason.response's meta.usage.breakdown for the webui's
// context-usage panel.
//
// The message half of the estimate lives in the transcript package
// (transcript.EstimateByKind): messages are the transcript's data, and its
// estimator must stay the one compaction budgets use, or the panel would
// disagree with the worker's own budget math. The system prompt and the tool
// list are assembled by the worker, not the transcript, so their attribution
// stays here.
//
// The split is an ESTIMATE: there is no tokenizer in this process (the worker
// is provider-agnostic), so each part is sized with the same byte-driven
// heuristic the transcript uses. The UI rescales the parts to the provider's
// reported input count, so only their PROPORTIONS need to be right.
package reason

import (
	"encoding/json"
	"math"

	"github.com/niq-run/niq/core/impl/reason/transcript"
	"github.com/niq-run/niq/core/itfs/llm"
)

// Breakdown keys. These are wire values the webui maps to localized labels;
// renaming one is a protocol change, not a refactor.
const (
	breakdownSystem              = "system"                // fixed system-prompt template
	breakdownProgramInstructions = "program.instructions"  // instruction programs folded in
	breakdownProgramPlaybooks    = "program.playbooks"     // playbook metadata folded in
	breakdownToolsBuiltin        = "tools.builtin"         // the worker's own sends (message/request/run)
	breakdownToolsProgram        = "tools.program"         // program worker's tools
	breakdownToolsMCP            = "tools.mcp"             // MCP / connector tools
	breakdownToolsWorkers        = "tools.workers"         // other infrastructure workers' tools
	breakdownToolsOther          = "tools.other"           // tool defs with no matching capability
	breakdownMessagesUser        = "messages.user"         // human / system inputs
	breakdownMessagesAssistant   = "messages.assistant"    // assistant text
	breakdownMessagesToolCalls   = "messages.tool_calls"   // assistant tool calls
	breakdownMessagesToolResults = "messages.tool_results" // tool result messages
	breakdownMessagesThinking    = "messages.thinking"     // assistant thinking blocks
)

// breakdownOrder is the canonical order sections are emitted in, mirroring the
// prompt's own layout (system prompt → tools → messages). The UI keeps its own
// grouping; this just keeps the wire payload stable between rounds.
var breakdownOrder = []string{
	breakdownSystem,
	breakdownProgramInstructions,
	breakdownProgramPlaybooks,
	breakdownToolsBuiltin,
	breakdownToolsProgram,
	breakdownToolsMCP,
	breakdownToolsWorkers,
	breakdownToolsOther,
	breakdownMessagesUser,
	breakdownMessagesAssistant,
	breakdownMessagesToolCalls,
	breakdownMessagesToolResults,
	breakdownMessagesThinking,
}

// contextSection is one line of the breakdown: a stable key plus the estimated
// tokens the prompt spends on it. Fields are exported and tagged because the
// section list is part of the persisted worker state (and of the context.status
// reply), not just a transient value.
type contextSection struct {
	Key    string `json:"key"`
	Tokens int    `json:"tokens"`
}

// contextBreakdown sizes the parts of a built request. Called once per round
// from prepareReasoning, which holds w.mu, so it may read the program state and
// the discovered capability universe directly.
func (w *BaseReasonWorker) contextBreakdown(c *llm.Context) []contextSection {
	if c == nil {
		return nil
	}
	sections := make(map[string]int)
	add := func(key string, n int) {
		if n > 0 {
			sections[key] += n
		}
	}

	// The system prompt is the template plus two optional program sections. Size
	// the base by rendering it alone, then take each section as the delta its
	// presence adds — that way the three parts always add up to the real prompt.
	data := w.promptData()
	base := renderSystemPrompt(templateData{WorkerID: data.WorkerID})
	withPlaybooks := renderSystemPrompt(templateData{WorkerID: data.WorkerID, Playbooks: data.Playbooks})
	withInstructions := renderSystemPrompt(templateData{WorkerID: data.WorkerID, Instructions: data.Instructions})
	baseTokens := transcript.EstimateText(base)
	add(breakdownSystem, baseTokens)
	add(breakdownProgramPlaybooks, transcript.EstimateText(withPlaybooks)-baseTokens)
	add(breakdownProgramInstructions, transcript.EstimateText(withInstructions)-baseTokens)

	// Tool definitions, attributed to the worker that offers them.
	for _, def := range c.Tools {
		add(w.toolSectionKey(def.Name), toolDefTokens(def))
	}

	// Transcript messages, split by what they carry.
	for kind, tokens := range transcript.EstimateByKind(c.Messages) {
		add(messageSectionKey(kind), tokens)
	}

	out := make([]contextSection, 0, len(sections))
	for _, key := range breakdownOrder {
		if n := sections[key]; n > 0 {
			out = append(out, contextSection{Key: key, Tokens: n})
		}
	}
	return out
}

// toolDefTokens estimates the tokens a tool definition costs in the request.
// The parameters travel as JSON, so their marshaled size is what counts.
func toolDefTokens(def llm.ToolDef) int {
	n := transcript.EstimateText(def.Name) + transcript.EstimateText(def.Description)
	if len(def.Parameters) > 0 {
		if b, err := json.Marshal(def.Parameters); err == nil {
			n += transcript.EstimateText(string(b))
		}
	}
	return n
}

// toolSectionKey maps a tool definition to the breakdown section its provider
// belongs to. The LLM tool list carries no source metadata, so the capability is
// resolved back by name (the same lookup dispatch uses).
func (w *BaseReasonWorker) toolSectionKey(name string) string {
	cap, ok := w.capabilityByToolName(name)
	if !ok {
		return breakdownToolsOther
	}
	switch {
	case cap.Source == w.ID():
		return breakdownToolsBuiltin
	case cap.SourceType == ProgramWorkerType:
		return breakdownToolsProgram
	case cap.SourceType == "mcp":
		return breakdownToolsMCP
	default:
		return breakdownToolsWorkers
	}
}

// messageSectionKey maps a transcript message kind to its breakdown section.
func messageSectionKey(kind transcript.Kind) string {
	switch kind {
	case transcript.KindAssistant:
		return breakdownMessagesAssistant
	case transcript.KindToolCall:
		return breakdownMessagesToolCalls
	case transcript.KindToolResult:
		return breakdownMessagesToolResults
	case transcript.KindThinking:
		return breakdownMessagesThinking
	default:
		return breakdownMessagesUser
	}
}

// scaleBreakdown turns the estimated sections into the wire form, rescaled to
// the provider's reported token counts so the parts add up to what the UI's
// headline shows (input + output). The output segment is exact — it is the
// round's reported generated tokens, not an estimate. Returns nil when there is
// nothing to attribute.
func scaleBreakdown(sections []contextSection, input, output int) []map[string]any {
	var estimated int
	for _, s := range sections {
		estimated += s.Tokens
	}
	if estimated <= 0 {
		return nil
	}
	scale := 1.0
	if input > 0 {
		scale = float64(input) / float64(estimated)
	}
	tokens := make([]int, len(sections))
	sum, largest := 0, 0
	for i, s := range sections {
		t := int(math.Round(float64(s.Tokens) * scale))
		if t < 1 {
			t = 1
		}
		tokens[i] = t
		sum += t
		if t > tokens[largest] {
			largest = i
		}
	}
	// Absorb rounding drift into the largest section so the parts sum to the
	// reported input count exactly (the UI divides each part by the window).
	// Skipped in the degenerate case where it would erase that section.
	if input > 0 && sum != input {
		if adjusted := tokens[largest] + (input - sum); adjusted >= 1 {
			tokens[largest] = adjusted
		}
	}
	out := make([]map[string]any, 0, len(sections)+1)
	for i, s := range sections {
		if tokens[i] <= 0 {
			continue
		}
		out = append(out, map[string]any{"key": s.Key, "tokens": tokens[i]})
	}
	if output > 0 {
		out = append(out, map[string]any{"key": "output", "tokens": output})
	}
	return out
}
