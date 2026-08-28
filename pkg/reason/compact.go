// Context budget and transcript compaction: the rolling-pressure mechanism for
// the
//
// A token ledger records the latest round-trip usage (InputTokens +
// OutputTokens snapshot, never accumulated) from EventDone.Message.Usage;
// against the model's ContextWindow it yields an occupancy ratio with two
// exits:
//
//	>= budget_soft -> guided: inject a reminder, the LLM calls the
//	   context_compress tool
//	>= budget_hard -> direct: the system compacts without waiting for the LLM
//
// How the transcript is compressed is the worker's Compactor (see below). The
// compaction runs under the caller's lock; the summarize call inside runs
// without it.
package reason

import (
	"context"
	"fmt"
	"log"
	"strings"

	llm "github.com/54c1/niq/core/llm"
)

// handleBudget updates the token ledger from a completed round's final message
// and acts on the budget thresholds: reminder (soft) or direct compaction
// (hard). Expects w.mu held.
func (w *BaseReasonWorker) handleBudget(ctx context.Context, msg llm.Message) {
	if msg.Usage == nil || w.contextWindow <= 0 {
		return
	}
	w.lastUsageTokens = msg.Usage.InputTokens + msg.Usage.OutputTokens

	ratio := float64(w.lastUsageTokens) / float64(w.contextWindow)
	switch {
	case ratio >= w.budgetHard:
		// Direct exit: compact without waiting for the LLM. Route through
		// worker.update like any other meta operation (single audit path).
		log.Printf("[reason %s] context hard budget %.0f%% (%d/%d tokens) - compacting",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.budgetReminded = false
		w.emitMetaUpdateRequest(ctx, "context.compress", nil)
	case ratio >= w.budgetSoft && !w.budgetReminded:
		// Guided exit: one reminder per crossing; the LLM decides.
		w.budgetReminded = true
		log.Printf("[reason %s] context soft budget %.0f%% (%d/%d tokens) - reminding",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.transcript.Apply(InputPatch{Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText,
				Text: fmt.Sprintf("[system] Context usage is at %d%% of the model window (%d/%d tokens). "+
					"Consider calling the context_compress tool to summarize older history before continuing.",
					int(ratio*100), w.lastUsageTokens, w.contextWindow)}},
		}}})
	case ratio < w.budgetSoft:
		w.budgetReminded = false
	}
}

// compactDirective returns the summarizer system prompt: program/config
// provided, else the built-in fallback.
func (w *BaseReasonWorker) compactDirective() string {
	if w.compactDirectiveOverride != "" {
		return w.compactDirectiveOverride
	}
	return fallbackCompactDirective
}

// ── Compactor: the replaceable compression strategy ──────────────────────────

// Compactor compresses a transcript under window pressure. reason only ever
// requires Compact — the operation invoked when the context is over budget or
// the provider rejects an over-long context. Additional operations (e.g.
// Rotate) are Compactor-specific extensions, reached by type assertion from
// whichever worker declared the corresponding tool; they are not part of the
// interface.
type Compactor interface {
	// Compact summarizes the older part of t into a digest, keeping what the
	// implementation decides. The caller already holds w.mu. How much history
	// to keep (tail) is the implementation's own policy, not a parameter.
	Compact(ctx context.Context, t Transcript, directive string) error
}

// DefaultCompactor is the shared LLM-summary implementation of Compactor. It
// holds exactly what it needs: the LLM provider for summarization and the
// tail policy. Both are injected at construction.
type DefaultCompactor struct {
	llm      llm.LLMProvider
	keepTail int
}

// NewDefaultCompactor builds the default compactor.
func NewDefaultCompactor(llm llm.LLMProvider, keepTail int) *DefaultCompactor {
	return &DefaultCompactor{llm: llm, keepTail: keepTail}
}

// fallbackCompactDirective is the summarizer system prompt when no
// program-provided directive is configured. The digest format is
// program-driven by design; this is only the built-in fallback template.
const fallbackCompactDirective = `Summarize the following reasoning transcript for continuation.
Preserve: the task/goal, decisions made and their reasons, established facts (paths, ids, versions, errors),
open TODOs, and the latest state. Drop: process detail, failed exploration steps (keep one-line lessons),
verbose tool output. Output only the summary, in a compact structured form.`

// Compact implements Compactor: snapshot, summarize (LLM, unlocked), apply.
func (c *DefaultCompactor) Compact(ctx context.Context, t Transcript, directive string) error {
	return c.compact(ctx, t, directive, c.keepTail, "compress")
}

// Rotate turns the page: summarize the current transcript as a carried digest
// and start a fresh context keeping only this turn's own closing call (the
// rotate tool's assistant tool_call + its [pending] placeholder), so the
// result stays visible to the model. Not part of the Compactor interface —
// called via type assertion by whoever declares the context_rotate tool. The
// directive (requested carry included) is prepared by the worker.
func (c *DefaultCompactor) Rotate(ctx context.Context, t Transcript, directive string) error {
	// keepTail=2: the rotating call's own message + placeholder.
	return c.compact(ctx, t, directive, 2, "rotate")
}

// compact is the shared core: snapshot via BeginEdit, optional previous-digest
// incremental mode, LLM summarize (no lock held), then apply via CommitEdit.
// If the summary fails, the edit is aborted and Apply inputs buffered during
// the window are preserved (merged by a later commit). On success a note is
// appended to the transcript so the model knows the operation completed — a
// meta tool never produces a tool_result, so without it the model would
// re-decide to compress every round.
func (c *DefaultCompactor) compact(ctx context.Context, t Transcript, directive string, keepTail int, label string) error {
	msgs := t.BeginEdit()
	projection := projectTranscript(msgs)
	previousDigest := currentDigest(msgs)

	if directive == "" {
		directive = fallbackCompactDirective
	}
	digest, err := c.summarize(ctx, projection, directive, previousDigest)
	if err != nil {
		t.AbortEdit()
		return fmt.Errorf("summarize: %w", err)
	}
	t.CommitEdit(digest, keepTail)
	log.Printf("[reason] transcript compacted (keepTail=%d, digest=%d chars, update=%v)",
		keepTail, len(digest), previousDigest != "")

	t.Apply(InputPatch{Messages: []llm.Message{{
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
func (c *DefaultCompactor) summarize(ctx context.Context, projection, directive, previousDigest string) (string, error) {
	if previousDigest != "" {
		directive = directive + "\n\nA previous summary exists; update it incrementally: merge new progress " +
			"into it, move finished items, keep earlier goals and constraints. Previous summary:\n" + previousDigest
	}
	resp, err := c.llm.Complete(ctx, &llm.CompletionRequest{
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
