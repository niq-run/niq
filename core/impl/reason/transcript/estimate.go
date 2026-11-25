package transcript

import "github.com/niq-run/niq/core/itfs/llm"

// Token-estimation constants.
//
// There is no tokenizer in this process (the transcript is provider-agnostic),
// so every number here is a deterministic, byte-driven approximation: ~4 bytes
// per token for text plus fixed per-message / per-content-block framing. The
// same constants size compaction budgets (EstimatedTokens, tailTokensCut) and
// attribute a rendered transcript's tokens to the kinds of content it carries.
// They are never used for billing or wire-format decisions.
//
// Compaction bounds the retained tail by a token budget instead of a fixed
// message count (a few large tool results can dwarf dozens of small messages);
// these constants are what that budget is measured in.
const (
	bytesPerToken      = 4
	messageOverhead    = 4
	blockOverhead      = 2
	imageTokenFloor    = 128
	imageTokensPerByte = 1024
)

// estimateMessageTokens returns a rough token estimate for one message: text at
// ~4 bytes/token plus fixed per-message and per-block overhead. Oversized text
// (already capped by the payload limiter) and modest images scale the count;
// the exact value is a heuristic bound, not a provider token count.
func estimateMessageTokens(m llm.Message) int {
	n := messageOverhead
	if m.ToolCallID != "" {
		n += 2
	}
	if m.ToolName != "" {
		n += len(m.ToolName)/bytesPerToken + 1
	}
	for _, b := range m.Content {
		switch b.Type {
		case llm.ContentText, llm.ContentThinking:
			n += blockOverhead + len(b.Text)/bytesPerToken
		case llm.ContentToolCall:
			n += blockOverhead + (len(b.ToolName)+len(b.ToolArguments))/bytesPerToken
		case llm.ContentImage:
			n += imageTokenFloor + len(b.Data)/imageTokensPerByte
		}
	}
	return n
}

// Kind classifies where a message's tokens go. It is a neutral taxonomy of
// transcript content — callers that render a breakdown map it to their own
// labels and colors; nothing here knows about a UI.
type Kind string

const (
	KindInput      Kind = "input"       // human / system input
	KindAssistant  Kind = "assistant"   // assistant prose
	KindToolCall   Kind = "tool_call"   // assistant tool-call arguments + framing
	KindToolResult Kind = "tool_result" // tool result message
	KindThinking   Kind = "thinking"    // assistant thinking blocks
)

// EstimateText returns a rough token estimate for a chunk of prompt text. An
// empty string costs nothing (so a caller can size an optional section as the
// difference between two renders).
func EstimateText(s string) int {
	if s == "" {
		return 0
	}
	return blockOverhead + len(s)/bytesPerToken
}

// EstimateByKind returns rough token estimates per message kind for a rendered
// transcript. Messages only: the system prompt and the tool definitions are
// assembled by the caller, not the transcript, so they are the caller's to
// attribute.
//
// The kinds partition exactly the same estimate EstimatedTokens reports: every
// message's overhead, id fields and content blocks land in exactly one kind
// (an assistant message's thinking and tool calls are split out of its prose).
func EstimateByKind(msgs []llm.Message) map[Kind]int {
	out := make(map[Kind]int)
	add := func(k Kind, n int) {
		if n > 0 {
			out[k] += n
		}
	}
	for _, m := range msgs {
		kind := KindInput
		switch m.Role {
		case llm.RoleAssistant:
			kind = KindAssistant
			if !hasTextOrThinking(m) && hasToolCall(m) {
				// A pure tool-call round: its framing belongs to the calls.
				kind = KindToolCall
			}
		case llm.RoleToolResult:
			kind = KindToolResult
		}
		// Per-message fields, mirroring estimateMessageTokens exactly.
		add(kind, messageOverhead)
		if m.ToolCallID != "" {
			add(kind, 2)
		}
		if m.ToolName != "" {
			add(kind, 1+len(m.ToolName)/bytesPerToken)
		}
		for _, b := range m.Content {
			switch b.Type {
			case llm.ContentText:
				add(kind, blockOverhead+len(b.Text)/bytesPerToken)
			case llm.ContentThinking:
				add(KindThinking, blockOverhead+len(b.Text)/bytesPerToken)
			case llm.ContentToolCall:
				add(KindToolCall,
					blockOverhead+(len(b.ToolName)+len(b.ToolArguments))/bytesPerToken)
			case llm.ContentImage:
				add(kind, imageTokenFloor+len(b.Data)/imageTokensPerByte)
			}
		}
	}
	return out
}

func hasTextOrThinking(m llm.Message) bool {
	for _, b := range m.Content {
		if (b.Type == llm.ContentText && b.Text != "") || b.Type == llm.ContentThinking {
			return true
		}
	}
	return false
}

func hasToolCall(m llm.Message) bool {
	for _, b := range m.Content {
		if b.Type == llm.ContentToolCall {
			return true
		}
	}
	return false
}
