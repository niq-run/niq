package reason

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/niq-run/niq/core/llm"
)

func textMsg(role llm.Role, text string) llm.Message {
	return llm.Message{Role: role, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: text}}}
}

func userMsg(text string) llm.Message      { return textMsg(llm.RoleUser, text) }
func assistantMsg(text string) llm.Message { return textMsg(llm.RoleAssistant, text) }

func toolCall(callID, name string) llm.ContentBlock {
	return llm.ContentBlock{Type: llm.ContentToolCall, ToolCallID: callID, ToolName: name}
}

// TestApplyLifecycle walks the full lifecycle through Apply and asserts the
// transcript order and shapes: input, assistant output, placeholders,
// in-place result replacement, park replacement, late result.
func TestApplyLifecycle(t *testing.T) {
	b := NewAccumulateTranscript()

	b.Apply(InputPatch{Messages: []llm.Message{userMsg("hi")}})
	b.Apply(AssistantOutputPatch{Message: assistantMsg("hello")})
	b.Apply(ToolPlaceholdersPatch{Calls: []llm.ContentBlock{toolCall("c1", "bash"), toolCall("c2", "read")}})
	b.Apply(ToolResultPatch{CallID: "c1", Name: "bash", Text: "0"})

	got := b.Render()
	if len(got) != 4 {
		t.Fatalf("got %d messages, want 4", len(got))
	}
	if got[0].Role != llm.RoleUser || got[1].Role != llm.RoleAssistant {
		t.Fatalf("order: got roles %q, %q", got[0].Role, got[1].Role)
	}
	// c1 resolved in place...
	if got[2].ToolCallID != "c1" || got[2].Content[0].Text != "0" {
		t.Fatalf("c1 not replaced in place: %+v", got[2])
	}
	// ...c2 still pending at its original position.
	if got[3].ToolCallID != "c2" || got[3].Content[0].Text != "[pending]" {
		t.Fatalf("c2 placeholder disturbed: %+v", got[3])
	}

	// Park c2: in-place replacement with the cause explanation.
	b.Apply(ToolParkedPatch{CallID: "c2", Name: "read", Cause: "timeout"})
	got = b.Render()
	if got[3].Content[0].Text != parkReason("timeout") {
		t.Fatalf("c2 park text: %q", got[3].Content[0].Text)
	}

	// Late result for parked c2: appended as a user message, not a second
	// tool_result for the same call_id.
	b.Apply(LateResultPatch{CallID: "c2", Name: "read", Text: "late-out", Cause: "timeout"})
	got = b.Render()
	if len(got) != 5 {
		t.Fatalf("got %d messages, want 5", len(got))
	}
	last := got[4]
	if last.Role != llm.RoleUser {
		t.Fatalf("late result role = %q, want user", last.Role)
	}
}

// TestPartialOutputPreserved verifies interrupted-round content lands in the
// transcript like any assistant output.
func TestPartialOutputPreserved(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(PartialOutputPatch{Message: llm.Message{
		Role: llm.RoleAssistant, StopReason: "interrupted",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "partial"}},
	}})
	got := b.Render()
	if len(got) != 1 || got[0].StopReason != "interrupted" {
		t.Fatalf("partial output lost: %+v", got)
	}
}

// TestStateRestoreRoundTrip verifies the snapshot cache round-trips.
func TestStateRestoreRoundTrip(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("hello")}})
	b.Apply(AssistantOutputPatch{Message: assistantMsg("hi")})

	state, err := b.State()
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	fresh := NewAccumulateTranscript()
	if err := fresh.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	a, b2 := b.Render(), fresh.Render()
	if len(a) != len(b2) {
		t.Fatalf("restored %d messages, want %d", len(b2), len(a))
	}
	for i := range a {
		if a[i].Role != b2[i].Role || a[i].StopReason != b2[i].StopReason ||
			a[i].ToolCallID != b2[i].ToolCallID {
			t.Fatalf("message %d mismatch:\n got %+v\nwant %+v", i, b2[i], a[i])
		}
	}
}

// TestToolResultUnknownCallIsSafe verifies a ToolResultPatch for an unknown
// call_id does not corrupt the transcript (no matching placeholder: no-op).
func TestToolResultUnknownCallIsSafe(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("hi")}})
	b.Apply(ToolResultPatch{CallID: "ghost", Name: "x", Text: "y"})
	if got := b.Render(); len(got) != 1 {
		t.Fatalf("unknown tool result should be a no-op, got %d messages", len(got))
	}
}

// TestCompactAppliesDigestAndKeepsTail verifies Compact replaces everything
// before the last keepTail messages with a digest message, tail preserved in
// order, and that it is a no-op when the transcript is already within the tail.
func TestCompactAppliesDigestAndKeepsTail(t *testing.T) {
	b := NewAccumulateTranscript()
	for i := 0; i < 5; i++ {
		b.Apply(InputPatch{Messages: []llm.Message{userMsg(fmt.Sprintf("m%d", i))}})
	}

	b.BeginEdit()
	b.CommitEdit("summary of m0-m2", 2)

	got := b.Render()
	if len(got) != 3 {
		t.Fatalf("got %d messages, want 3 (digest + 2 tail)", len(got))
	}
	if got[0].Role != llm.RoleUser || !strings.Contains(got[0].Content[0].Text, "summary of m0-m2") {
		t.Fatalf("digest head missing: %+v", got[0])
	}
	if got[1].Content[0].Text != "m3" || got[2].Content[0].Text != "m4" {
		t.Fatalf("tail order disturbed: %q, %q", got[1].Content[0].Text, got[2].Content[0].Text)
	}

	// No-op when everything fits in the tail.
	b.BeginEdit()
	b.CommitEdit("again", 10)
	if len(b.Render()) != 3 {
		t.Fatal("compact within tail must be a no-op")
	}
}

// TestCompactAlignsCutToPairing verifies the cut point never leaves orphan
// tool_results at the tail head: a keepTail that would cut between an
// assistant(tool_calls) and its placeholder absorbs the placeholder into the
// compacted side.
func TestCompactAlignsCutToPairing(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("q")}})
	b.Apply(AssistantOutputPatch{Message: llm.Message{
		Role: llm.RoleAssistant, StopReason: "tool_calls",
		Content: []llm.ContentBlock{{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"}},
	}})
	b.Apply(ToolPlaceholdersPatch{Calls: []llm.ContentBlock{
		{Type: llm.ContentToolCall, ToolCallID: "c1", ToolName: "bash"},
	}})
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("after")}})

	// keepTail=1 would cut before the placeholder (orphan tool_result);
	// alignment must move the cut past it.
	b.BeginEdit()
	b.CommitEdit("digest", 1)

	got := b.Render()
	if got[0].Role != llm.RoleUser || !strings.Contains(got[0].Content[0].Text, "digest") {
		t.Fatalf("digest head missing: %+v", got[0])
	}
	// The tail must not open with an orphan tool_result (pairing invariant).
	if len(got) > 1 && got[1].Role == llm.RoleToolResult {
		t.Fatalf("tail must not start with orphan tool_result: %+v", got)
	}
	last := got[len(got)-1]
	if last.Content[0].Text != "after" {
		t.Fatalf("recent messages must be preserved: %+v", got)
	}
}

// TestCompactTurnsThePage verifies keepTail = 0 starts a fresh episode from
// the digest alone.
func TestCompactTurnsThePage(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("a")}})
	b.Apply(AssistantOutputPatch{Message: assistantMsg("b")})

	b.BeginEdit()
	b.CommitEdit("episode summary", 0)

	got := b.Render()
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1 (digest only)", len(got))
	}
	if !strings.Contains(got[0].Content[0].Text, "episode summary") {
		t.Fatalf("digest missing: %+v", got[0])
	}
}

// TestEditBuffersApply verifies the meta-edit semantics: Apply inputs during
// BeginEdit..CommitEdit are buffered (not appended), and are merged after the
// digest on commit, so they are neither lost nor torn by the edit's overwrite.
func TestEditBuffersApply(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("a")}})
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("b")}})

	// Begin an edit; the snapshot is the pre-edit messages.
	b.BeginEdit()

	// While editing, an external input arrives: it must be buffered, not
	// appended to the visible transcript.
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("during-edit")}})

	// Commit: digest replaces all but the tail, then the buffered input is
	// appended (after the digest + tail).
	b.CommitEdit("summary of a..", 0)

	got := b.Render()
	if len(got) != 2 {
		t.Fatalf("expected digest + buffered input, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Content[0].Text, "summary of a..") {
		t.Fatalf("digest head missing: %+v", got[0])
	}
	if len(got[1].Content) == 0 || got[1].Content[0].Text != "during-edit" {
		t.Fatalf("buffered Apply input should be merged after digest: %+v", got[1])
	}
}

// TestAbortEditPreserves verifies AbortEdit clears the editing state and the
// transcript keeps its prior content (buffered inputs are preserved for a
// later commit, not silently dropped into the visible transcript).
func TestAbortEditPreserves(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("a")}})

	b.BeginEdit()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("during")}})
	b.AbortEdit()

	// Abort: no digest applied; the visible transcript is unchanged.
	got := b.Render()
	if len(got) != 1 || got[0].Content[0].Text != "a" {
		t.Fatalf("abort should leave transcript unchanged, got %+v", got)
	}
}

// TestSanitizeDanglingToolCalls verifies tool_calls are stripped from assistant
// messages lacking a following tool_result, while paired calls are kept.
func TestSanitizeDanglingToolCalls(t *testing.T) {
	// Dangling meta tool call (no following tool result): the tool_calls block
	// is dropped, thinking/text kept.
	msgs := []llm.Message{{
		Role: llm.RoleAssistant,
		Content: []llm.ContentBlock{
			{Type: llm.ContentThinking, Text: "think"},
			toolCall("c1", "context.compress"),
			{Type: llm.ContentText, Text: "text"},
		},
	}}
	sanitizeDanglingToolCalls(msgs)
	if len(msgs[0].Content) != 2 {
		t.Fatalf("want 2 blocks (thinking+text), got %d", len(msgs[0].Content))
	}
	for _, b := range msgs[0].Content {
		if b.Type == llm.ContentToolCall {
			t.Fatal("dangling tool_call not stripped")
		}
	}

	// Paired tool call (followed by a tool result): kept.
	msgs = []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.ContentBlock{toolCall("c2", "bash")}},
		{Role: llm.RoleToolResult, ToolCallID: "c2", Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}},
	}
	sanitizeDanglingToolCalls(msgs)
	if len(msgs[0].Content) != 1 || msgs[0].Content[0].Type != llm.ContentToolCall {
		t.Fatal("paired tool_call must be kept")
	}
}

// TestCommitEditStripsDanglingMetaToolCall verifies a compaction that keeps a
// meta tool call (which never produces a result) in its tail yields a valid
// transcript: the tool_calls block is stripped after the edit.
func TestCommitEditStripsDanglingMetaToolCall(t *testing.T) {
	b := NewAccumulateTranscript()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("hi")}})
	b.Apply(AssistantOutputPatch{Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentBlock{toolCall("m1", "context.compress")},
	}})

	b.BeginEdit()
	b.CommitEdit("digest", 1)
	msgs := b.Render()
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2 (digest + assistant)", len(msgs))
	}
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant {
		t.Fatalf("last message role = %q, want assistant", last.Role)
	}
	if len(last.Content) != 0 {
		t.Fatalf("dangling meta tool_call survived CommitEdit: %+v", last.Content)
	}
}

// ── payload truncation ──

// TestPayloadTruncationToolResult verifies an oversized tool result is capped
// to the transcript's payload limit: head kept, truncation note appended,
// original size carried, and the kept text + note fit within the cap.
func TestPayloadTruncationToolResult(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(64))
	b.Apply(ToolPlaceholdersPatch{Calls: []llm.ContentBlock{toolCall("c1", "bash")}})
	b.Apply(ToolResultPatch{CallID: "c1", Name: "bash", Text: strings.Repeat("x", 1000)})

	got := b.Render()
	if len(got) != 1 {
		t.Fatalf("got %d messages, want 1", len(got))
	}
	text := got[0].Content[0].Text
	if !strings.Contains(text, "[truncated, kept") {
		t.Fatalf("oversized tool result must carry a truncation note: %q", text)
	}
	if !strings.Contains(text, "of 1000 bytes") {
		t.Fatalf("note must carry the original size: %q", text)
	}
	if len(text) > 64 {
		t.Fatalf("truncated payload exceeds cap: %d bytes > 64", len(text))
	}
	if !strings.HasPrefix(text, strings.Repeat("x", 1)) {
		t.Fatalf("truncated payload must keep the head: %q", text)
	}
}

// TestPayloadTruncationRuneSafe verifies the byte-based cut never splits a
// multi-byte UTF-8 rune (e.g. Chinese).
func TestPayloadTruncationRuneSafe(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(32))
	b.Apply(InputPatch{Messages: []llm.Message{userMsg(strings.Repeat("中", 100))}})

	text := b.Render()[0].Content[0].Text
	if !utf8.ValidString(text) {
		t.Fatalf("truncated payload must not split UTF-8: %q", text)
	}
	if !strings.Contains(text, "[truncated, kept") {
		t.Fatalf("oversized input must be truncated: %q", text)
	}
}

// TestPayloadTruncationInput verifies an oversized external input message is
// capped the same way as a tool result.
func TestPayloadTruncationInput(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(64))
	b.Apply(InputPatch{Messages: []llm.Message{userMsg(strings.Repeat("y", 200))}})

	text := b.Render()[0].Content[0].Text
	if !strings.Contains(text, "[truncated, kept") || len(text) > 64 {
		t.Fatalf("oversized input not truncated: %q (%d bytes)", text, len(text))
	}
}

// TestPayloadTruncationLateResult verifies an oversized late-arriving tool
// result is capped as well.
func TestPayloadTruncationLateResult(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(64))
	b.Apply(LateResultPatch{CallID: "c1", Name: "bash", Text: strings.Repeat("z", 300), Cause: "timeout"})

	text := b.Render()[0].Content[0].Text
	if !strings.Contains(text, "[truncated, kept") || len(text) > 64 {
		t.Fatalf("oversized late result not truncated: %q (%d bytes)", text, len(text))
	}
}

// TestPayloadNotTruncatedAssistantOutput verifies model-produced output is
// applied verbatim: truncation is for external payloads only.
func TestPayloadNotTruncatedAssistantOutput(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(64))
	big := strings.Repeat("a", 500)
	b.Apply(AssistantOutputPatch{Message: assistantMsg(big)})

	got := b.Render()
	if len(got) != 1 || got[0].Content[0].Text != big {
		t.Fatalf("assistant output must be preserved verbatim: %+v", got)
	}
}

// TestPayloadUnderLimitUntouched verifies payloads within the cap pass through
// unchanged.
func TestPayloadUnderLimitUntouched(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(64))
	small := "ok"
	b.Apply(ToolPlaceholdersPatch{Calls: []llm.ContentBlock{toolCall("c1", "bash")}})
	b.Apply(ToolResultPatch{CallID: "c1", Name: "bash", Text: small})

	got := b.Render()
	if len(got) != 1 || got[0].Content[0].Text != small {
		t.Fatalf("under-limit payload must be untouched: %+v", got)
	}
}

// TestPayloadDefaultLimit verifies the default transcript caps over-limit
// payloads at DefaultMaxPayloadBytes, and that max <= 0 falls back to it.
func TestPayloadDefaultLimit(t *testing.T) {
	for _, b := range []*AccumulateTranscript{
		NewAccumulateTranscript(),
		NewAccumulateTranscript(WithMaxPayloadBytes(0)),
		NewAccumulateTranscript(WithMaxPayloadBytes(-1)),
	} {
		b.Apply(InputPatch{Messages: []llm.Message{userMsg(strings.Repeat("x", DefaultMaxPayloadBytes+1))}})
		text := b.Render()[0].Content[0].Text
		if !strings.Contains(text, "[truncated, kept") {
			t.Fatalf("default cap must truncate over-limit payload")
		}
		if len(text) > DefaultMaxPayloadBytes {
			t.Fatalf("truncated payload exceeds default cap: %d > %d", len(text), DefaultMaxPayloadBytes)
		}
	}
}

// TestPayloadTruncationDuringEdit verifies inputs buffered across an edit are
// truncated too — a large payload must not bypass the cap through the edit
// buffer.
func TestPayloadTruncationDuringEdit(t *testing.T) {
	b := NewAccumulateTranscript(WithMaxPayloadBytes(64))
	b.Apply(InputPatch{Messages: []llm.Message{userMsg("a")}})

	b.BeginEdit()
	b.Apply(InputPatch{Messages: []llm.Message{userMsg(strings.Repeat("q", 300))}})
	b.CommitEdit("digest", 0)

	got := b.Render()
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2 (digest + buffered)", len(got))
	}
	text := got[1].Content[0].Text
	if !strings.Contains(text, "[truncated, kept") || len(text) > 64 {
		t.Fatalf("buffered input during edit must be truncated: %q (%d bytes)", text, len(text))
	}
}
