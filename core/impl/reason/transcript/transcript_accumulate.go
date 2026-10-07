// AccumulateTranscript: the default transcript implementation. A flat
// transcript of llm.Messages; digest messages may appear among them after a
// CommitEdit. Concurrency-safe on its own: each method locks internally, and
// edits (BeginEdit..CommitEdit) run their computation without holding the lock
// while buffering concurrent external inputs.
package transcript

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/niq-run/niq/core/itfs/llm"
)

// DefaultMaxPayloadBytes caps a single text payload folded into the transcript
// (a tool result, an external input message). Larger payloads are truncated to
// the head with a truncation note so one event cannot flood the context.
const DefaultMaxPayloadBytes = 20 * 1024

// Rough token-estimation constants. Compaction bounds the retained tail by a
// token budget instead of a fixed message count (a few large tool results can
// dwarf dozens of small messages), but there is no tokenizer available at
// commit time (the transcript is provider-agnostic). These constants give a
// deterministic, byte-driven approximation: ~4 bytes per token for typical
// text, plus fixed per-message and per-content-block overhead. The estimate is
// used only for budget sizing, never for billing or wire-format decisions.
const (
	bytesPerToken      = 4
	messageOverhead    = 4 // role + separators + id fields
	blockOverhead      = 2 // per content block framing
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

// digestMessage wraps a compacted transcript summary as the head message of
// the new projection. User role: it must read as system-provided context to
// the model without violating any pairing invariant. The [context digest]
// prefix marks the message so update-mode summarization can detect a carried
// digest.
func digestMessage(digest string) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: "[context digest] " + digest}},
	}
}

// AccumulateTranscript holds the working transcript messages. It is
// concurrency-safe on its own: every method locks internally, and edits
// (BeginEdit..CommitEdit) run their computation without holding the lock while
// buffering concurrent Apply calls.
type AccumulateTranscript struct {
	mu sync.Mutex

	messages     []llm.Message
	editing      bool          // an edit is in progress
	pendingInput []llm.Message // Apply inputs buffered during the edit

	maxPayloadBytes int // per-message text cap; <= 0 means no truncation
}

// AccumulateOption configures an AccumulateTranscript at construction.
type AccumulateOption func(*AccumulateTranscript)

// WithMaxPayloadBytes caps text payloads folded into the transcript at max
// bytes; oversized payloads are truncated to their head with a truncation
// note. max <= 0 leaves the default cap.
func WithMaxPayloadBytes(max int) AccumulateOption {
	return func(b *AccumulateTranscript) {
		if max > 0 {
			b.maxPayloadBytes = max
		}
	}
}

// NewAccumulateTranscript creates an empty transcript with the default payload
// cap, optionally overridden by opts.
func NewAccumulateTranscript(opts ...AccumulateOption) *AccumulateTranscript {
	b := &AccumulateTranscript{maxPayloadBytes: DefaultMaxPayloadBytes}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// Apply folds one lifecycle fact into the transcript. If an edit is in
// progress, the input is buffered (merged on CommitEdit), so it is neither
// lost nor torn by the edit's overwrite.
func (b *AccumulateTranscript) Apply(input TranscriptPatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applyLocked(input)
}

func (b *AccumulateTranscript) applyLocked(input TranscriptPatch) {
	if b.editing {
		// During an edit, only external inputs and late results may still arrive
		// (the current round has ended; its tool calls were rejected).
		// Both are append-only additions that should survive the edit's
		// overwrite, so they are buffered and merged on commit. Any other
		// variant is a stale worker-lifecycle action and is dropped.
		switch in := input.(type) {
		case InputPatch:
			for _, m := range in.Messages {
				b.pendingInput = append(b.pendingInput, b.limitMessageText(m))
			}
		case LateResultPatch:
			if in.Text != "" {
				b.pendingInput = append(b.pendingInput,
					b.limitMessageText(lateResultMessage(in.CallID, in.Name, in.Text, in.Cause)))
			}
		}
		return
	}
	switch in := input.(type) {
	case InputPatch:
		msgs := make([]llm.Message, 0, len(in.Messages))
		for _, m := range in.Messages {
			msgs = append(msgs, b.limitMessageText(m))
		}
		b.messages = append(b.messages, msgs...)
	case AssistantOutputPatch:
		b.messages = append(b.messages, in.Message)
	case PartialOutputPatch:
		b.messages = append(b.messages, in.Message)
	case ToolPlaceholdersPatch:
		for _, call := range in.Calls {
			b.messages = append(b.messages, placeholderMessage(call))
		}
	case ToolResultPatch:
		b.messages = replacePlaceholder(b.messages, in.CallID,
			b.limitMessageText(toolResultMessage(in.CallID, in.Name, in.Text, in.IsErr)))
	case ToolParkedPatch:
		b.messages = replacePlaceholder(b.messages, in.CallID,
			b.limitMessageText(toolResultMessage(in.CallID, in.Name, parkReason(in.Cause), false)))
	case LateResultPatch:
		if in.Text != "" {
			b.messages = append(b.messages,
				b.limitMessageText(lateResultMessage(in.CallID, in.Name, in.Text, in.Cause)))
		}
	default:
		// Unknown variants are ignored: the sealed algebra grows at the
		// interface, old snapshots stay readable.
	}
}

// limitMessageText truncates oversized text blocks in a message to the
// transcript's payload cap, keeping the head of each and appending a
// truncation note. Only payload-carrying messages (tool results, external
// inputs) are routed through here; model-produced output is applied verbatim.
// The caller's message is never mutated: truncation copies message and content
// blocks (copy-on-write), so concurrent producers sharing a slice stay intact.
func (b *AccumulateTranscript) limitMessageText(m llm.Message) llm.Message {
	if b.maxPayloadBytes <= 0 {
		return m
	}
	truncated := false
	for _, blk := range m.Content {
		if blk.Type == llm.ContentText && len(blk.Text) > b.maxPayloadBytes {
			truncated = true
			break
		}
	}
	if !truncated {
		return m
	}
	out := m
	out.Content = append([]llm.ContentBlock(nil), m.Content...)
	for i := range out.Content {
		if out.Content[i].Type == llm.ContentText && len(out.Content[i].Text) > b.maxPayloadBytes {
			out.Content[i].Text = truncateText(out.Content[i].Text, b.maxPayloadBytes)
		}
	}
	return out
}

// truncateNote is appended to a truncated payload; both %d placeholders are
// byte counts (kept, original).
const truncateNote = "...[truncated, kept %d of %d bytes]"

// truncateText keeps the head of a text under a byte cap and appends a note
// carrying the original size. The cut falls on a rune boundary so multi-byte
// UTF-8 (e.g. Chinese) is never split mid-character. Room for the note is
// reserved up front (sized for the worst-case kept digit count), so the kept
// head plus note fit within maxBytes; for absurdly small caps the head shrinks
// instead of the note overflowing the entire budget.
func truncateText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	noteLen := len("...[truncated, kept ") + len(strconv.Itoa(maxBytes)) +
		len(" of ") + len(strconv.Itoa(len(text))) + len(" bytes]")
	room := maxBytes - noteLen
	if room < 1 {
		room = maxBytes/2 + 1
	}
	if room > len(text) {
		room = len(text)
	}
	for room > 0 && !utf8.RuneStart(text[room]) {
		room--
	}
	return text[:room] + fmt.Sprintf(truncateNote, room, len(text))
}

// Render returns the transcript for the next LLM round. The returned slice
// must not be mutated.
func (b *AccumulateTranscript) Render() []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.messages
}

// EstimatedTokens returns a rough estimate of the current transcript's token
// size (messages only). See the package-level estimateMessageTokens heuristic.
func (b *AccumulateTranscript) EstimatedTokens() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	var n int
	for _, m := range b.messages {
		n += estimateMessageTokens(m)
	}
	return n
}

// BeginEdit starts an edit: marks the transcript as editing and returns a
// snapshot. The lock is released before returning, so the caller can compute
// off-transcript (e.g. an LLM summary); Apply calls during the edit are
// buffered.
func (b *AccumulateTranscript) BeginEdit() []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.editing = true
	return b.messages
}

// CommitEdit applies an edit: it rewrites the transcript to a digest head,
// retaining only the most recent messages that fit within the tailTokens token
// budget (pairing-preserving), then merges the Apply inputs buffered during
// the edit. No-op when the whole transcript already fits within the budget
// (no digest is applied); tailTokens <= 0 keeps nothing (fresh episode).
func (b *AccumulateTranscript) CommitEdit(digest string, tailTokens int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.editing {
		return false
	}
	b.editing = false

	cut := tailTokensCut(b.messages, tailTokens)
	rewrote := cut > 0
	if rewrote {
		b.messages = append([]llm.Message{digestMessage(digest)}, b.messages[cut:]...)
	}
	if len(b.pendingInput) > 0 {
		b.messages = append(b.messages, b.pendingInput...)
		b.pendingInput = nil
	}
	sanitizeDanglingToolCalls(b.messages)
	return rewrote
}

// CommitRotate applies an edit that turns the page to a clean conversation: it
// retains only the most recent keepRounds user/assistant rounds (a round is one
// user input plus the assistant's response to it) and discards tool messages.
// tool_result role messages are dropped and tool-call blocks are stripped from
// assistant messages, so the fresh episode reads as a digest head followed by
// a few exchanges rather than a pile of tool traffic. Consistent-message
// normalization (merging consecutive same-role messages) is left to the
// provider layer, which knows its API contract; the transcript keeps the
// retained messages as-is. keepRounds <= 0 keeps nothing (fresh episode). No-op
// if no edit is in progress.
func (b *AccumulateTranscript) CommitRotate(digest string, keepRounds int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.editing {
		return false
	}
	b.editing = false

	tail := lastRoundsClean(b.messages, keepRounds)
	rewrote := true
	if len(b.messages) == 0 {
		rewrote = false
	}
	b.messages = append([]llm.Message{digestMessage(digest)}, tail...)
	if len(b.pendingInput) > 0 {
		b.messages = append(b.messages, b.pendingInput...)
		b.pendingInput = nil
	}
	sanitizeDanglingToolCalls(b.messages)
	return rewrote
}

// lastRoundsClean returns the most recent keepRounds user/assistant rounds of
// msgs as clean messages: tool_result role messages are dropped and assistant
// tool-call blocks are stripped, so only the conversational spine (user inputs
// and assistant responses) survives. Messages are kept as-is (no same-role
// merging); consecutive-message normalization is the provider layer's
// responsibility. keepRounds <= 0 (or no user messages) yields nothing.
func lastRoundsClean(msgs []llm.Message, keepRounds int) []llm.Message {
	if keepRounds <= 0 {
		return nil
	}
	// Locate round boundaries: each round starts at a user message.
	userIdx := make([]int, 0, 4)
	for i, m := range msgs {
		if m.Role == llm.RoleUser {
			userIdx = append(userIdx, i)
		}
	}
	if len(userIdx) == 0 {
		return nil
	}
	start := 0
	first := len(userIdx) - keepRounds
	if first > start {
		start = userIdx[first]
	}

	var out []llm.Message
	for i := start; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case llm.RoleToolResult:
			continue // discard tool messages
		case llm.RoleAssistant:
			m = stripToolCallBlocks(m)
			if len(m.Content) == 0 {
				continue // an assistant message that was only a tool call
			}
		}
		out = append(out, m)
	}
	return out
}

// stripToolCallBlocks returns a copy of m with all tool-call content blocks
// removed, keeping text/thinking. The original message is not mutated.
func stripToolCallBlocks(m llm.Message) llm.Message {
	out := m
	if !hasContentBlocks(out.Content, llm.ContentToolCall) {
		return out
	}
	kept := make([]llm.ContentBlock, 0, len(out.Content))
	for _, b := range out.Content {
		if b.Type != llm.ContentToolCall {
			kept = append(kept, b)
		}
	}
	out.Content = kept
	return out
}

func hasContentBlocks(blocks []llm.ContentBlock, typ llm.ContentBlockType) bool {
	for _, b := range blocks {
		if b.Type == typ {
			return true
		}
	}
	return false
}

// sanitizeDanglingToolCalls strips tool_calls from assistant messages that
// have no following tool_result. Compaction can orphan a tool call two ways:
// a cut landing between an assistant tool_calls and its results, or a meta
// tool call (compress/rotate) that never produces a result. A dangling
// tool_calls message is rejected by providers ("assistant with tool_calls
// must be followed by tool messages"), so the call is dropped while the
// message's text/thinking is kept.
func sanitizeDanglingToolCalls(msgs []llm.Message) {
	for i := range msgs {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		hasToolCalls := false
		for _, b := range msgs[i].Content {
			if b.Type == llm.ContentToolCall {
				hasToolCalls = true
				break
			}
		}
		if !hasToolCalls {
			continue
		}
		if i+1 < len(msgs) && msgs[i+1].Role == llm.RoleToolResult {
			continue // paired with a following tool result
		}
		kept := msgs[i].Content[:0]
		for _, b := range msgs[i].Content {
			if b.Type != llm.ContentToolCall {
				kept = append(kept, b)
			}
		}
		msgs[i].Content = kept
	}
}

// AbortEdit cancels an edit without applying it: clears the editing state
// and leaves buffered inputs unmerged (the main transcript stays as it was;
// buffered inputs are preserved here so a later commit does not lose them, and
// they are appended by the next successful commit). No-op if no edit is in
// progress.
func (b *AccumulateTranscript) AbortEdit() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.editing {
		return
	}
	b.editing = false
	// Keep pendingInput as-is; a later commit will merge it. This preserves
	// inputs received during an aborted edit rather than dropping them.
}

// tailTokensCut returns the cut index (start of the retained tail) for a
// compaction under a tailTokens token budget. The most recent message is
// always kept; older messages are added while the running token estimate stays
// within budget. The cut is then snapped backward so it never opens a tail
// with an orphan tool_result: a tool call and its results are kept together
// even when that pushes the tail slightly over budget, so a pairing is never
// truncated by the cut.
func tailTokensCut(msgs []llm.Message, tailTokens int) int {
	n := len(msgs)
	if n == 0 || tailTokens <= 0 {
		return n // keep nothing (fresh episode / digest only)
	}

	cut := n - 1 // start by keeping the newest message
	tokens := estimateMessageTokens(msgs[cut])
	for cut > 0 {
		mt := estimateMessageTokens(msgs[cut-1])
		if tokens+mt > tailTokens {
			break
		}
		tokens += mt
		cut--
	}

	// Never let the tail open with an orphan tool_result: if the budget ran out
	// right after an assistant tool_call (its results would be kept but its
	// assistant compacted), pull the assistant in so the pairing survives. A
	// tool_result always immediately follows its assistant tool_call (the
	// placeholders are inserted together), so stepping back to the first
	// non-tool_result lands on the owning assistant and keeps its whole group.
	for cut < n && msgs[cut].Role == llm.RoleToolResult {
		cut--
	}
	if cut < 0 {
		cut = 0
	}
	return cut
}

// accumulateState is the serializable projection cache. The field set must
// only grow (older blobs stay readable).
type accumulateState struct {
	Messages []llm.Message `json:"messages"`
}

// State serializes the projection cache.
func (b *AccumulateTranscript) State() ([]byte, error) {
	return json.Marshal(accumulateState{Messages: b.messages})
}

// Restore rehydrates the transcript from a State blob.
func (b *AccumulateTranscript) Restore(state []byte) error {
	var s accumulateState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("transcript restore: %w", err)
	}
	b.messages = s.Messages
	return nil
}

// parkReason returns the explanatory text shown in the [pending] placeholder
// when a call is parked, describing why the reasoner stopped waiting on it.
func parkReason(cause string) string {
	switch cause {
	case "timeout":
		return "Tool call timed out; reasoner proceeded without waiting"
	case "input":
		return "Tool call interrupted by new input; reasoner proceeded without waiting"
	case "abort":
		return "Tool call aborted"
	case "reminder":
		return "Tool call interrupted by reminder; reasoner proceeded without waiting"
	case "restart":
		return "Tool call did not survive the restart; reasoner is no longer waiting"
	default:
		return "Tool call parked; reasoner proceeded"
	}
}

// toolResultMessage builds a tool_result message for a tool call with the
// given outcome text and error flag.
func toolResultMessage(callID, name, text string, isError bool) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: callID,
		ToolName:   name,
		IsError:    isError,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: text}},
	}
}

// placeholderMessage builds the initial [pending] tool_result entry.
func placeholderMessage(call llm.ContentBlock) llm.Message {
	return llm.Message{
		Role:       llm.RoleToolResult,
		ToolCallID: call.ToolCallID,
		ToolName:   call.ToolName,
		Content:    []llm.ContentBlock{{Type: llm.ContentText, Text: "[pending]"}},
	}
}

// lateResultMessage appends a plain user message for a late-arriving tool
// result on a parked call. Adding a RoleToolResult message would create a
// duplicate [tool] entry for the same call_id, which LLM APIs reject.
func lateResultMessage(callID, name, text, cause string) llm.Message {
	label := "Late result for tool call"
	switch cause {
	case "timeout":
		label = "Timed-out tool call"
	case "input":
		label = "Interrupted tool call"
	case "abort":
		label = "Aborted tool call"
	case "reminder":
		label = "Interrupted tool call"
	case "restart":
		label = "Tool call interrupted by restart"
	}
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: fmt.Sprintf("[%s %s (%s) returned late]: %s", label, callID, name, text)}},
	}
}

// replacePlaceholder replaces the tool_result placeholder for callID, if any.
// No-op when no placeholder matches.
func replacePlaceholder(msgs []llm.Message, callID string, msg llm.Message) []llm.Message {
	for i := range msgs {
		if msgs[i].Role == llm.RoleToolResult && msgs[i].ToolCallID == callID {
			msgs[i] = msg
			return msgs
		}
	}
	return msgs
}
