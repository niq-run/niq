// LLM error handling policy: each structured error type is answered with a
// specific recovery action instead of a blind retry.
//
//	context overflow (ErrorContextLength) → request a compression
//	rate limit (ErrorRateLimit)          → schedule a reminder-based backoff
//	auth / bad request (4xx permanent)   → fail the round
//	everything else                      → retry with backoff
//
// The policy dispatcher is decideLLMError, called from openStream's retry
// loop. Both recovery actions are event-driven through the bus
// (worker.update for compression, tool.request→timer.reminder for
// rate limits) so they stay auditable and never block the reason goroutine
// with a synchronous sleep.
package reason

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/reason/transcript"
)

// decideLLMError classifies a failed LLM call and returns whether to retry it
// (with backoff) or to surface err as terminal. It may schedule recovery work
// as a side effect: a compression request for context overflows, or a
// reminder-based backoff for rate limits.
func (w *BaseReasonWorker) decideLLMError(reasonCtx context.Context, traceID string, callErr error) (retry bool, err error) {
	var llmErr *llm.LLMError
	if !errors.As(callErr, &llmErr) {
		// Unclassified: not enough to judge, treat as transient.
		return true, callErr
	}

	switch llmErr.Type {
	case llm.ErrorContextLength:
		// Context is over the hard limit: retrying cannot help. Ask the
		// worker to compress via the convention event (single auditable
		// path), and return a non-retriable error so this round ends; the
		// worker responds by shrinking its transcript and scheduling the next
		// round itself.
		w.emitContextCompress(reasonCtx)
		return false, callErr
	case llm.ErrorRateLimit:
		// Decide here, at 429 detection time, whether more retries remain: if
		// the budget is exhausted no reminder is scheduled and the round
		// fails; otherwise a reminder fires later to retry (see below).
		return false, w.scheduleRateLimitBackoff(traceID, callErr)
	case llm.ErrorAuthFailed, llm.ErrorBadRequest:
		// Permanent: a retry cannot change the outcome.
		return false, callErr
	default:
		// Rate limits, timeouts, provider-side blips and other transient LLM
		// errors all deserve a few backoff attempts.
		return true, callErr
	}
}

// ---------------------------------------------------------------------------
// Rate-limit (HTTP 429) backoff
// ---------------------------------------------------------------------------

const (
	// maxRateLimitRetries is how many times a 429 is re-attempted via a
	// scheduled reminder before the round gives up.
	maxRateLimitRetries = 5
	// rateLimitBackoffDuration is the delay before each retry.
	rateLimitBackoffDuration = time.Minute
)

// errRateLimitBackoff ends the current round quietly: a retry reminder has
// been scheduled, and reasoning resumes when it fires.
var errRateLimitBackoff = fmt.Errorf("rate limit: backoff scheduled")

// scheduleRateLimitBackoff records one more 429. The 429 is always recorded in
// the transcript so the LLM sees it. If the retry budget remains, a reminder
// is also scheduled after rateLimitBackoffDuration and errRateLimitBackoff is
// returned so the round pauses; when the reminder fires later the LLM already
// has the 429 in its context. If the budget is exhausted, no timer is
// scheduled and the original error is returned so the round fails.
func (w *BaseReasonWorker) scheduleRateLimitBackoff(traceID string, callErr error) error {
	w.mu.Lock()
	w.rateLimitAttempts++
	attempt := w.rateLimitAttempts
	exhausted := attempt > maxRateLimitRetries
	w.mu.Unlock()

	// Always record the 429 in the transcript, whether a retry is scheduled or
	// the budget is exhausted, so the LLM knows what happened.
	note := fmt.Sprintf("[system] API rate limit (HTTP 429) hit; retrying after %s (attempt %d/%d)",
		rateLimitBackoffDuration, attempt, maxRateLimitRetries)
	if exhausted {
		note = fmt.Sprintf("[system] API rate limit (HTTP 429) hit; retries exhausted after %d attempts, giving up", attempt)
	}
	w.transcript.Apply(transcript.InputPatch{Messages: []llm.Message{{
		Role:    llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: note}},
	}}})

	if exhausted {
		log.Printf("[reason %s] rate limit retries exhausted (%d), giving up", w.ID(), attempt)
		w.resetRateLimitBackoff()
		return callErr
	}

	log.Printf("[reason %s] rate limited (%d/%d), retrying in %s",
		w.ID(), attempt, maxRateLimitRetries, rateLimitBackoffDuration)
	w.dispatchElapseReminder(traceID, attempt)
	return errRateLimitBackoff
}

// dispatchElapseReminder asks the timer worker to fire a reminder after the
// backoff duration, routed through the bus so the retry is auditable. The
// 429 itself was already recorded in the transcript by
// scheduleRateLimitBackoff, so the reminder only needs to wake the round up.
func (w *BaseReasonWorker) dispatchElapseReminder(traceID string, attempt int) {
	callID := fmt.Sprintf("niq_rl_%d_%d", time.Now().UnixNano(), attempt)
	evt := event.New(event.TypeToolRequest, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"call_id":   callID,
		"name":      "elapse",
		"arguments": map[string]any{
			"duration_ms": rateLimitBackoffDuration.Milliseconds(),
			"purpose": fmt.Sprintf("API rate limit (HTTP 429) hit; retrying after %s, attempt %d/%d",
				rateLimitBackoffDuration, attempt, maxRateLimitRetries),
		},
	})
	evt.TraceID = traceID
	if err := w.Channel.Send(context.Background(), evt, "timer"); err != nil {
		log.Printf("[reason %s] failed to schedule rate-limit retry reminder: %v", w.ID(), err)
	}
}

// resetRateLimitBackoff clears the rate-limit retry state after a successful
// round or when the budget is exhausted.
func (w *BaseReasonWorker) resetRateLimitBackoff() {
	w.mu.Lock()
	w.rateLimitAttempts = 0
	w.mu.Unlock()
}
