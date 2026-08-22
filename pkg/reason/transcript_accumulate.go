// AccumulateTranscript: the default transcript implementation. A flat
// transcript of llm.Messages; digest messages may appear among them after a
// CommitEdit. Concurrency-safe on its own: each method locks internally, and
// edits (BeginEdit..CommitEdit) run their computation without holding the lock
// while buffering concurrent external inputs.
package reason

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/54c1/niq/core/llm"
)

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
}

// NewAccumulateTranscript creates an empty transcript.
func NewAccumulateTranscript() *AccumulateTranscript {
	return &AccumulateTranscript{}
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
			b.pendingInput = append(b.pendingInput, in.Messages...)
		case LateResultPatch:
			if in.Text != "" {
				b.pendingInput = append(b.pendingInput,
					lateResultMessage(in.CallID, in.Name, in.Text, in.Cause))
			}
		}
		return
	}
	switch in := input.(type) {
	case InputPatch:
		b.messages = append(b.messages, in.Messages...)
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
			toolResultMessage(in.CallID, in.Name, in.Text, in.IsErr))
	case ToolParkedPatch:
		b.messages = replacePlaceholder(b.messages, in.CallID,
			toolResultMessage(in.CallID, in.Name, parkReason(in.Cause), false))
	case LateResultPatch:
		if in.Text != "" {
			b.messages = append(b.messages, lateResultMessage(in.CallID, in.Name, in.Text, in.Cause))
		}
	default:
		// Unknown variants are ignored: the sealed algebra grows at the
		// interface, old snapshots stay readable.
	}
}

// Render returns the transcript for the next LLM round. The returned slice
// must not be mutated.
func (b *AccumulateTranscript) Render() []llm.Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.messages
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

// CommitEdit applies an edit: replaces all but the last keepTail messages
// with a digest (alignment-corrected), then merges the Apply inputs buffered
// during the edit. No-op if no edit is in progress.
func (b *AccumulateTranscript) CommitEdit(digest string, keepTail int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.editing {
		return
	}
	b.editing = false

	n := len(b.messages)
	if n > keepTail {
		cut := alignCutToPairing(b.messages, n-keepTail)
		b.messages = append([]llm.Message{digestMessage(digest)}, b.messages[cut:]...)
	}
	if len(b.pendingInput) > 0 {
		b.messages = append(b.messages, b.pendingInput...)
		b.pendingInput = nil
	}
	sanitizeDanglingToolCalls(b.messages)
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

// alignCutToPairing moves a cut point forward past tool_result messages that
// belong to an assistant tool_calls message left of the cut: a tail starting
// with orphan tool_results would violate the pairing invariant. The tail
// shrinks (more history is compacted) - never grows.
func alignCutToPairing(msgs []llm.Message, cut int) int {
	for cut < len(msgs) && msgs[cut].Role == llm.RoleToolResult {
		cut++
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
