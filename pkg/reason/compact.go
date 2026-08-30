// Context budget: the rolling-pressure mechanism that watches transcript
// occupancy and emits the context.compress convention event when the model's
// window is exceeded. The mechanism only fires the event; how the transcript
// is actually shrunk is the worker's own strategy — it handles the event and
// edits its transcript. A token ledger records the latest round-trip usage
// (InputTokens + OutputTokens snapshot, never accumulated) from
// EventDone.Message.Usage; against the model's ContextWindow it yields an
// occupancy ratio with two exits:
//
//	>= budget_soft -> guided: inject a reminder, the LLM decides to call
//	   the context.compress tool
//	>= budget_hard -> direct: the mechanism emits the context.compress event
//	   and returns; the worker shrinks the transcript asynchronously.
package reason

import (
	"context"
	"fmt"
	"log"

	llm "github.com/niq-run/niq/core/llm"
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
		// Direct exit: the mechanism emits the context.compress convention
		// event and returns. The worker responds by shrinking its transcript;
		// the mechanism performs no edit and does no bookkeeping here (it
		// cannot observe the async handler). Single audit path via the bus.
		log.Printf("[reason %s] context hard budget %.0f%% (%d/%d tokens) - requesting compress",
			w.ID(), ratio*100, w.lastUsageTokens, w.contextWindow)
		w.budgetReminded = false
		w.emitContextCompress(ctx)
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

// ContextCompressOp is the convention every reason worker honors: under window
// pressure the mechanism emits a worker.update with this op, and the worker
// responds by shrinking its own transcript. The mechanism fires the event; it
// never performs or books the edit itself — that is the worker's strategy,
// reached by handling this event. Rotate and any other context op are the
// worker's own extras; the mechanism does not emit them.
const ContextCompressOp = "context.compress"
