// The default worker's context strategy: it shrinks this worker's transcript
// in response to the context.compress convention event. This
// is NOT a reason-package interface implementation — it is the worker's own
// strategy. The mechanism fires context.compress under window pressure; this
// package responds by editing the transcript directly (via the exported
// Transcript / LLMProvider / TailTokens accessors) and books the completion
// itself. A custom reason worker replaces this by handling the event its own
// way; there is no second, parallel compression.
package reason

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/impl/reason/transcript"
	llm "github.com/niq-run/niq/core/itfs/llm"
)

// FallbackCompactDirective is the built-in summarizer system prompt used when
// no program-provided directive is configured. The digest format is
// program-driven by design; this template is only the default fallback for
// the worker's own compress strategy.
const FallbackCompactDirective = `Summarize the following reasoning transcript for continuation.
Preserve: the task/goal, decisions made and their reasons, established facts (paths, ids, versions, errors),
open TODOs, and the latest state. Drop: process detail, failed exploration steps (keep one-line lessons),
verbose tool output. Output only the summary, in a compact structured form.`

// programDigestHint keeps the digest from becoming a second, stale copy of the
// worker's programs. Those live out of band, in the system prompt (instructions
// in full, playbooks by reference), so the summary should not reproduce them —
// except a change that so far exists only as a [system] note in the transcript:
// that note may not have reached the prompt yet (the fold happens only when a
// compression really rewrites the transcript), so its essentials must survive.
const programDigestHint = "\n\nThe worker's mounted programs live in the system prompt, supplied out of band: " +
	"do not reproduce their text in the summary. Exception: a program change recorded only " +
	"as a [system] note in the transcript may not have reached the system prompt yet — keep " +
	"the essentials of such a note (the rule it states), without copying long text verbatim."

// compactTranscript rewrites w's transcript to shrink it: it summarizes older
// history into a carried digest (context.compress) and keeps a token-budgeted,
// pairing-preserved tail. The summarize call runs without the mechanism lock;
// the transcript self-buffers concurrent Apply inputs during the edit and merges
// them on commit. It returns whether the transcript was actually rewritten: a
// compress can be a no-op when the whole transcript already fits within the tail
// token budget (no digest head applied), which callers must observe rather than
// report a misleading success.
func compactTranscript(w *reason.BaseReasonWorker, ctx context.Context, directive string) (bool, error) {
	t := w.Transcript()
	msgs := t.BeginEdit()
	projection := projectTranscript(msgs)
	previousDigest := currentDigest(msgs)

	if directive == "" {
		directive = FallbackCompactDirective
	}

	prov := w.LLMProvider()
	if prov == nil {
		t.AbortEdit()
		return false, fmt.Errorf("no LLM provider available for summarization")
	}
	digest, err := summarize(ctx, prov, projection, directive, previousDigest)
	if err != nil {
		t.AbortEdit()
		return false, fmt.Errorf("summarize: %w", err)
	}

	head := []llm.Message{digestMessage(digest)}
	tail := transcript.TailTokensBudget(msgs, w.TailTokens())
	// The digest head is only added when something was actually folded away;
	// otherwise the whole transcript already fits within the tail budget and the
	// edit is a no-op (no digest, no fold).
	rewrote := len(tail) < len(msgs)
	if !rewrote {
		head = nil
	}
	// A real rewrite installs the digest as the transcript's new head, and that is
	// what CommitTranscript reads to record the reset boundary: the next round
	// pays a cache reset anyway, so it is also where a pending program change
	// reaches the system prompt. A no-op compression commits with no head and
	// therefore changes no bytes, leaving the prompt (and its warm cache) alone.
	w.CommitTranscript(head, tail)
	log.Printf("[reason] transcript compacted (tailTokens=%d, digest=%d chars, update=%v, rewrote=%v)",
		w.TailTokens(), len(digest), previousDigest != "", rewrote)
	return rewrote, nil
}

// compactionNote is the human-readable description of a completed compaction,
// used as the tool result text the model sees for the compress tool (and the
// completion echoed to observers). rewrote distinguishes a real rewrite from a
// no-op (already within budget). On a no-op it reports the current transcript
// token estimate and the tail budget, so the model can judge whether its
// tail_tokens parameter is larger than necessary.
func compactionNote(rewrote bool, usedTokens, tailTokens int) string {
	if !rewrote {
		// Record the attempted action factually: a compression was triggered
		// (there is a matching assistant tool_call for this result), but the
		// transcript was already within the tail budget, so nothing was folded.
		return fmt.Sprintf("[system] A context compression was attempted but skipped: the transcript is ~%d tokens, within the %d-token tail budget, so nothing was folded.",
			usedTokens, tailTokens)
	}
	return "[system] context compressed: older messages were summarized into a digest. Continue the task with the recent context. The system prompt now carries this worker's currently mounted programs."
}

// autoCompressDoneNote is the system note appended to the transcript after an
// auto-fired context compression (hard budget / no tool call). Unlike the
// model-invoked path there is no tool pair to carry the outcome, so the note
// is the only in-context record that the compression ran. An auto compression
// is only fired when the hard budget threshold was crossed, so it always
// rewrites the transcript; there is intentionally no "skipped" variant here.
const autoCompressDoneNote = "[system] auto context compression done: the transcript now begins with a digest of the older history, followed by the most recent retained messages."

// summarize calls the LLM over a stream and accumulates the digest from the
// streamed text. Some gateways only support streaming (rejecting non-streaming
// requests outright), so summarization must go through CompleteStream rather
// than Complete. With a previous digest present it runs in update mode: merge
// new progress into the old summary instead of rebuilding from scratch, so
// early goals and constraints survive repeated compactions.
func summarize(ctx context.Context, prov llm.LLMProvider, projection, directive, previousDigest string) (string, error) {
	if previousDigest != "" {
		directive = directive + "\n\nA previous summary exists; update it incrementally: merge new progress " +
			"into it, move finished items, keep earlier goals and constraints. Previous summary:\n" + previousDigest
	}
	stream, err := prov.CompleteStream(ctx, &llm.CompletionRequest{
		Context: &llm.Context{
			SystemPrompt: directive,
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: projection}}}},
		},
	})
	if err != nil {
		return "", err
	}

	var text strings.Builder
	var streamErr error
	for {
		evt, ok := stream.Next()
		if !ok {
			break
		}
		switch e := evt.(type) {
		case llm.EventTextDelta:
			text.WriteString(e.Delta)
		case llm.EventError:
			streamErr = e.Err
		}
	}
	if streamErr != nil {
		return "", streamErr
	}
	if strings.TrimSpace(text.String()) != "" {
		return text.String(), nil
	}
	// Fallback for providers that deliver the digest in the closing message
	// rather than as text deltas. End/Abort always populate the result channel
	// before closing events, so Result returns promptly here.
	msg, err := stream.Result(ctx)
	if err != nil {
		return "", err
	}
	for _, block := range msg.Content {
		if block.Type == llm.ContentText && block.Text != "" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("summarizer returned no text")
}

// digestPrefix marks the head message produced by a compaction.
const digestPrefix = "[context digest]"

// digestMessage wraps a compacted transcript summary as the head message of
// the new projection. User role: it must read as system-provided context to
// the model without violating any pairing invariant. The [context digest]
// prefix marks the message so update-mode summarization can detect a carried
// digest (currentDigest parses it back). This head construction is the
// compactor's policy, not the transcript's — the transcript only assembles the
// head + tail it is handed.
func digestMessage(digest string) llm.Message {
	return llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: digestPrefix + " " + digest}},
	}
}

// currentDigest extracts the carried digest text from a transcript whose head
// is a digest message (the result of a previous compaction), if any.
func currentDigest(msgs []llm.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	m := msgs[0]
	if m.Role != llm.RoleUser || len(m.Content) == 0 || m.Content[0].Type != llm.ContentText {
		return ""
	}
	if !strings.HasPrefix(m.Content[0].Text, digestPrefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(m.Content[0].Text, digestPrefix))
}

// projectTranscript renders a lossy-but-faithful projection of the transcript
// for summarization: images and thinking stripped, tool results truncated,
// one line per message.
func projectTranscript(msgs []llm.Message) string {
	const maxToolResult = 2000

	var b strings.Builder
	for i, m := range msgs {
		switch m.Role {
		case llm.RoleToolResult:
			text := ""
			if len(m.Content) > 0 && m.Content[0].Text != "" {
				text = m.Content[0].Text
			}
			if len(text) > maxToolResult {
				text = text[:maxToolResult] + "...[truncated]"
			}
			fmt.Fprintf(&b, "[%d] tool_result %s: %s\n", i, m.ToolName, text)
		default:
			for _, block := range m.Content {
				switch block.Type {
				case llm.ContentText:
					fmt.Fprintf(&b, "[%d] %s: %s\n", i, m.Role, block.Text)
				case llm.ContentToolCall:
					fmt.Fprintf(&b, "[%d] %s tool_call %s(%s)\n", i, m.Role, block.ToolName, block.ToolArguments)
				case llm.ContentImage:
					fmt.Fprintf(&b, "[%d] %s image(%d bytes, omitted)\n", i, m.Role, len(block.Data))
				case llm.ContentThinking:
					// stripped: reasoning process, not carried into digests
				}
			}
		}
	}
	return b.String()
}
