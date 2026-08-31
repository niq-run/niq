// The default worker's context strategy: it shrinks this worker's transcript
// in response to the context.compress / context.rotate convention events. This
// is NOT a reason-package interface implementation — it is the worker's own
// strategy. The mechanism fires context.compress under window pressure; this
// package responds by editing the transcript directly (via the exported
// Transcript / LLMProvider / KeepTail accessors) and books the completion
// itself. A custom reason worker replaces this by handling the event its own
// way; there is no second, parallel compression.
package reason

import (
	"context"
	"fmt"
	"log"
	"strings"

	llm "github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/reason"
	"github.com/niq-run/niq/pkg/reason/transcript"
)

// FallbackCompactDirective is the built-in summarizer system prompt used when
// no program-provided directive is configured. The digest format is
// program-driven by design; this template is only the default fallback for
// the worker's own compress strategy.
const FallbackCompactDirective = `Summarize the following reasoning transcript for continuation.
Preserve: the task/goal, decisions made and their reasons, established facts (paths, ids, versions, errors),
open TODOs, and the latest state. Drop: process detail, failed exploration steps (keep one-line lessons),
verbose tool output. Output only the summary, in a compact structured form.`

// compactTranscript rewrites w's transcript to shrink it. When rotate is false
// it summarizes older history into a carried digest (context.compress); when
// rotate is true it starts a fresh context keeping only this turn's own pair
// (context.rotate). The summarize call runs without the mechanism lock; the
// transcript self-buffers concurrent Apply inputs during the edit and merges
// them on commit. On success a note is appended so the model knows the
// operation ran and does not re-decide to compress every round.
func compactTranscript(w *reason.BaseReasonWorker, ctx context.Context, rotate bool, directive string) error {
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
		return fmt.Errorf("no LLM provider available for summarization")
	}
	digest, err := summarize(ctx, prov, projection, directive, previousDigest)
	if err != nil {
		t.AbortEdit()
		return fmt.Errorf("summarize: %w", err)
	}

	keepTail := 2 // rotate keeps the rotating call's own message + placeholder
	if !rotate {
		keepTail = w.KeepTail()
	}
	t.CommitEdit(digest, keepTail)
	log.Printf("[reason] transcript compacted (rotate=%t, keepTail=%d, digest=%d chars, update=%v)",
		rotate, keepTail, len(digest), previousDigest != "")

	label := "compress"
	if rotate {
		label = "rotate"
	}
	t.Apply(transcript.InputPatch{Messages: []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: compactionNote(label)}},
	}}})
	return nil
}

// compactionNote is the transcript message appended after a successful
// compaction so the model can proceed instead of re-deciding to compress.
func compactionNote(label string) string {
	if label == "rotate" {
		return "[system] episode rotated: history was compacted into a carried digest and a fresh context started. Continue the task."
	}
	return "[system] context compressed: older messages were summarized into a digest. Continue the task with the recent context."
}

// summarize calls the LLM once, non-streaming. With a previous digest present
// it runs in update mode: merge new progress into the old summary instead of
// rebuilding from scratch, so early goals and constraints survive repeated
// compactions.
func summarize(ctx context.Context, prov llm.LLMProvider, projection, directive, previousDigest string) (string, error) {
	if previousDigest != "" {
		directive = directive + "\n\nA previous summary exists; update it incrementally: merge new progress " +
			"into it, move finished items, keep earlier goals and constraints. Previous summary:\n" + previousDigest
	}
	resp, err := prov.Complete(ctx, &llm.CompletionRequest{
		Context: &llm.Context{
			SystemPrompt: directive,
			Messages:     []llm.Message{{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: projection}}}},
		},
	})
	if err != nil {
		return "", err
	}
	for _, block := range resp.Message.Content {
		if block.Type == llm.ContentText && block.Text != "" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("summarizer returned no text")
}

// digestPrefix marks the head message produced by a compaction.
const digestPrefix = "[context digest]"

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
