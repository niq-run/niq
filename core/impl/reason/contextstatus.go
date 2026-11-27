// Context state: the worker's own accounting of its context window.
//
// The transcript is the worker's living memory, and the provider's charge for
// it — prompt tokens, cache hits, generated tokens — is only observable at the
// end of a round and cannot be re-derived from the transcript afterwards. So
// the last round's numbers are kept in the worker state and travel in the
// worker's snapshot: after a restart the transcript comes back, and its
// accounting comes back with it.
//
// context.status exposes that state on the bus (a request to the worker's own
// event, e.g. from the control plane or a peer). It is strictly read-only: it
// schedules no round and writes no history.
package reason

import (
	"log"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
	llm "github.com/niq-run/niq/core/itfs/llm"
)

// TypeContextStatus is the read-only query for this worker's context state.
const TypeContextStatus event.EventType = "context.status"

// contextUsage is the last completed round's context accounting. It is part of
// the persisted worker state (reasonState.ContextUsage) because the provider's
// numbers are its only source: a restart can restore the transcript but not
// what the model charged for it.
type contextUsage struct {
	InputTokens         int  `json:"input_tokens"`
	OutputTokens        int  `json:"output_tokens"`
	TotalTokens         int  `json:"total_tokens"`
	CacheReadTokens     *int `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens *int `json:"cache_creation_tokens,omitempty"`
	// Breakdown is the round's estimated prompt composition, unscaled. The
	// query rescales it to the reported counts, exactly as reason.response's
	// meta.usage.breakdown does, so both views agree.
	Breakdown []contextSection `json:"breakdown,omitempty"`
	// Rounds counts the reasoning rounds this worker has completed since
	// birth (across restarts) and At is when the last one finished (unix secs).
	Rounds uint64 `json:"rounds"`
	At     int64  `json:"at"`
}

// registerContextStatus registers the context.status query. It is deliberately
// not Internal: "how full is this worker's context, and with what" is exactly
// what the control plane and cooperating workers need to ask. It is also not
// part of the reason family's tool scope, so it never becomes one of the
// model's own tools.
func (w *BaseReasonWorker) registerContextStatus() {
	w.Register(baseworker.Extension{
		Event: TypeContextStatus,
		Response: baseworker.ObjectSchema(map[string]any{
			"context_window":        map[string]any{"type": "number", "description": "the active model's context window (0 = unknown)"},
			"occupancy_percent":     map[string]any{"type": "number", "description": "used_tokens / context_window, when the window is known"},
			"used_tokens":           map[string]any{"type": "number", "description": "prompt + generated tokens of the last completed round"},
			"input_tokens":          map[string]any{"type": "number", "description": "prompt tokens of the last round (cached tokens included)"},
			"output_tokens":         map[string]any{"type": "number"},
			"total_tokens":          map[string]any{"type": "number"},
			"cache_read_tokens":     map[string]any{"type": "number", "description": "prompt tokens served from the prefix cache"},
			"cache_creation_tokens": map[string]any{"type": "number", "description": "prompt tokens written to the prefix cache"},
			"cache_hit_percent":     map[string]any{"type": "number", "description": "cache_read_tokens / input_tokens"},
			"breakdown": baseworker.ArraySchema(baseworker.ObjectSchema(map[string]any{
				"key":    map[string]any{"type": "string"},
				"tokens": map[string]any{"type": "number"},
			})),
			"transcript_tokens": map[string]any{"type": "number", "description": "estimated size of the transcript as it stands now"},
			"messages":          map[string]any{"type": "number", "description": "messages in the transcript now"},
			"budget_soft":       map[string]any{"type": "number", "description": "occupancy ratio that triggers a compress reminder"},
			"budget_hard":       map[string]any{"type": "number", "description": "occupancy ratio that triggers an automatic compress"},
			"rounds":            map[string]any{"type": "number", "description": "completed reasoning rounds (0 before the first)"},
			"last_round_at":     map[string]any{"type": "number", "description": "unix seconds of the last completed round"},
		}),
		Description: "Read this worker's context state: the model window and occupancy, the last round's " +
			"prompt/generated/cache tokens, the prompt's composition (system prompt, programs, tools, " +
			"messages, output), the transcript's current size and the compression budgets. Read-only.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(evt event.Event) {
		w.handleContextStatus(evt)
	})
}

// recordContextUsage stores the round's accounting. Called from finishReasoning
// with w.mu held, for every completed round — including tool-call rounds, which
// publish no reason.response and would otherwise leave the state behind.
func (w *BaseReasonWorker) recordContextUsage(msg llm.Message) {
	if msg.Usage == nil {
		return
	}
	u := msg.Usage
	rounds := uint64(1)
	if w.contextUsage != nil {
		rounds = w.contextUsage.Rounds + 1
	}
	w.contextUsage = &contextUsage{
		InputTokens:         u.InputTokens,
		OutputTokens:        u.OutputTokens,
		TotalTokens:         u.TotalTokens,
		CacheReadTokens:     copyIntPtr(u.CacheReadTokens),
		CacheCreationTokens: copyIntPtr(u.CacheCreationTokens),
		Breakdown:           w.roundBreakdown,
		Rounds:              rounds,
		At:                  time.Now().Unix(),
	}
}

// handleContextStatus answers a context.status request. It runs under w.mu (the
// event loop holds it for the whole dispatch), so it reads the state directly.
func (w *BaseReasonWorker) handleContextStatus(evt event.Event) {
	snapshot := w.contextStatusSnapshot()
	w.ReplyCompleted(evt.WorkerId, evt.RequestId, snapshot, evt.TraceID)
	if rounds, ok := snapshot["rounds"].(uint64); ok {
		log.Printf("[reason %s] context.status: rounds=%d", w.ID(), rounds)
	}
}

// contextStatusSnapshot renders the current context state in the shape
// context.status replies with. Before the first completed round there is no
// usage to report, but the window, the transcript size and the budgets are
// still meaningful.
func (w *BaseReasonWorker) contextStatusSnapshot() map[string]any {
	snapshot := map[string]any{
		"context_window":    w.contextWindow,
		"transcript_tokens": w.transcript.EstimatedTokens(),
		"messages":          len(w.transcript.Render()),
		"budget_soft":       w.budgetSoft,
		"budget_hard":       w.budgetHard,
	}
	cu := w.contextUsage
	if cu == nil {
		snapshot["rounds"] = uint64(0)
		return snapshot
	}
	prompt, completion := promptAndCompletion(&llm.Usage{
		InputTokens:  cu.InputTokens,
		OutputTokens: cu.OutputTokens,
		TotalTokens:  cu.TotalTokens,
	})
	used := cu.TotalTokens
	if used <= 0 {
		used = prompt + completion
	}
	snapshot["rounds"] = cu.Rounds
	snapshot["last_round_at"] = cu.At
	snapshot["input_tokens"] = cu.InputTokens
	snapshot["output_tokens"] = cu.OutputTokens
	snapshot["total_tokens"] = cu.TotalTokens
	snapshot["used_tokens"] = used
	if cu.CacheReadTokens != nil {
		snapshot["cache_read_tokens"] = *cu.CacheReadTokens
		if pct, ok := cacheHitPercent(cu.CacheReadTokens, prompt); ok {
			snapshot["cache_hit_percent"] = pct
		}
	}
	if cu.CacheCreationTokens != nil {
		snapshot["cache_creation_tokens"] = *cu.CacheCreationTokens
	}
	if w.contextWindow > 0 {
		snapshot["occupancy_percent"] = float64(used) / float64(w.contextWindow) * 100
	}
	if b := scaleBreakdown(cu.Breakdown, prompt, completion); len(b) > 0 {
		snapshot["breakdown"] = b
	}
	return snapshot
}

// promptAndCompletion splits a usage into the prompt size and the tokens the
// model generated. The reported total wins when it is present, so the parts add
// up to the headline the UI shows.
func promptAndCompletion(u *llm.Usage) (prompt, completion int) {
	completion = u.OutputTokens
	if u.TotalTokens > u.InputTokens {
		completion = u.TotalTokens - u.InputTokens
	}
	return u.InputTokens, completion
}

// cacheHitPercent returns the share of the prompt served from the prefix cache.
// ok is false when there is no prompt to divide by. Clamped so a misreporting
// provider cannot render as more than 100%.
func cacheHitPercent(cacheRead *int, promptTokens int) (float64, bool) {
	if cacheRead == nil || promptTokens <= 0 {
		return 0, false
	}
	pct := float64(*cacheRead) / float64(promptTokens) * 100
	if pct > 100 {
		pct = 100
	}
	return pct, true
}

// copyIntPtr copies an optional counter, so the persisted state does not alias
// provider-owned memory.
func copyIntPtr(p *int) *int {
	if p == nil {
		return nil
	}
	n := *p
	return &n
}
