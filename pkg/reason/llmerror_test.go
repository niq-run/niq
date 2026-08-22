package reason

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/54c1/niq/core/event"
	llm "github.com/54c1/niq/core/llm"
)

func TestScheduleRateLimitBackoff(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)
	cause := &llm.LLMError{Type: llm.ErrorRateLimit, Message: "429 too many requests"}

	// The first maxRateLimitRetries hits schedule a reminder and pause the round.
	for i := 1; i <= maxRateLimitRetries; i++ {
		err := w.scheduleRateLimitBackoff("tr", cause)
		if !errors.Is(err, errRateLimitBackoff) {
			t.Fatalf("attempt %d: want errRateLimitBackoff, got %v", i, err)
		}
		if w.rateLimitAttempts != i {
			t.Fatalf("attempt %d: rateLimitAttempts=%d, want %d", i, w.rateLimitAttempts, i)
		}
		// Each retry records a 429 note in the transcript at detection time.
		if n := len(w.transcript.Render()); n != i {
			t.Fatalf("attempt %d: transcript has %d messages, want %d", i, n, i)
		}
	}

	// The next hit is terminal: the original error surfaces and state resets —
	// no reminder is dispatched, but the give-up is still recorded.
	err := w.scheduleRateLimitBackoff("tr", cause)
	if !errors.Is(err, cause) {
		t.Fatalf("exhausted: want original error, got %v", err)
	}
	if w.rateLimitAttempts != 0 {
		t.Fatalf("exhausted: rateLimitAttempts=%d, want 0", w.rateLimitAttempts)
	}
	msgs := w.transcript.Render()
	if n := len(msgs); n != maxRateLimitRetries+1 {
		t.Fatalf("exhausted: transcript has %d messages, want %d", n, maxRateLimitRetries+1)
	}
	if got := msgs[len(msgs)-1].Content[0].Text; !strings.Contains(got, "giving up") {
		t.Fatalf("exhausted note must say giving up, got: %q", got)
	}

	// Exactly maxRateLimitRetries elapse requests went to the timer worker.
	reqs := ch.eventsOf(event.TypeToolRequested)
	if len(reqs) != maxRateLimitRetries {
		t.Fatalf("dispatched %d tool.requested, want %d", len(reqs), maxRateLimitRetries)
	}
	for i, evt := range reqs {
		name, _ := evt.Payload["name"].(string)
		if name != "elapse" {
			t.Fatalf("req %d: want name=elapse, got %q", i, name)
		}
		args, _ := evt.Payload["arguments"].(map[string]any)
		if args == nil {
			t.Fatalf("req %d: missing arguments", i)
		}
		if d := args["duration_ms"]; d != int64(rateLimitBackoffDuration.Milliseconds()) {
			t.Fatalf("req %d: duration_ms=%v, want %d", i, d, rateLimitBackoffDuration.Milliseconds())
		}
		if p, _ := args["purpose"].(string); !strings.Contains(p, "429") {
			t.Fatalf("req %d: purpose %q must describe the 429", i, p)
		}
	}
}

func TestDecideLLMError(t *testing.T) {
	ch := newTestChannel()
	w := newTestWorker(nil, ch)
	ctx := context.Background()

	cases := []struct {
		name         string
		err          *llm.LLMError
		wantRetry    bool
		wantCompress bool
	}{
		{"rate limit schedules backoff", &llm.LLMError{Type: llm.ErrorRateLimit, Message: "429"}, false, false},
		{"context length compresses", &llm.LLMError{Type: llm.ErrorContextLength, Message: "over limit"}, false, true},
		{"bad request fails", &llm.LLMError{Type: llm.ErrorBadRequest, Message: "bad"}, false, false},
		{"auth fails", &llm.LLMError{Type: llm.ErrorAuthFailed, Message: "no key"}, false, false},
		{"timeout retries", &llm.LLMError{Type: llm.ErrorTimeout, Message: "slow"}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(ch.eventsOf(event.TypeWorkerUpdate))
			retry, _ := w.decideLLMError(ctx, "tr", tc.err)
			if retry != tc.wantRetry {
				t.Fatalf("retry=%v, want %v", retry, tc.wantRetry)
			}
			gotCompress := len(ch.eventsOf(event.TypeWorkerUpdate)) > before
			if gotCompress != tc.wantCompress {
				t.Fatalf("compress=%v, want %v", gotCompress, tc.wantCompress)
			}
		})
	}
}

// rateLimitProvider returns ErrorRateLimit for its first `failures` stream
// calls, then a plain text response.
type rateLimitProvider struct {
	failures int
	calls    int
}

func (p *rateLimitProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	p.calls++
	if p.calls <= p.failures {
		return nil, &llm.LLMError{Type: llm.ErrorRateLimit, Message: "429 too many requests"}
	}
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextDelta{Delta: "ok"})
	es.Push(llm.EventTextEnd{})
	es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}})
	return es, nil
}
func (p *rateLimitProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return nil, &llm.LLMError{Type: llm.ErrorRateLimit, Message: "429 too many requests"}
}
func (p *rateLimitProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

func TestRateLimitRoundTrip(t *testing.T) {
	prov := &rateLimitProvider{failures: 1}
	w, ch, _ := startWorker(t, prov)

	// First user input: the round hits a 429 and pauses quietly.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello"})
	waitCond(t, testTimeout, func() bool {
		return ch.hasStopReason("rate_limited")
	}, "reason.end rate_limited after 429")

	if n := len(ch.eventsOf("reason.response")); n != 0 {
		t.Fatalf("429 must not surface an error response, got %d", n)
	}

	w.mu.Lock()
	attempts := w.rateLimitAttempts
	w.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("rateLimitAttempts=%d, want 1 after first 429", attempts)
	}

	// The 429 is already recorded in the transcript at detection time, before
	// the reminder fires.
	if !transcriptHasText(w.transcript.Render(), "429") {
		t.Fatal("429 must be recorded in the transcript at detection time")
	}

	// Simulate the timer worker firing the reminder. It is handled like any
	// other reminder: injected into the transcript, then a retry round starts.
	ch.in <- event.New("timer.reminder", "timer", map[string]any{
		"caller_id": w.ID(),
		"result":    fmt.Sprintf(`{"tick_type":"reminder","purpose":"API rate limit (HTTP 429) hit; retrying after 5s, attempt 1/%d","duration_ms":5000}`, maxRateLimitRetries),
	})

	// The retry succeeds and the round completes.
	waitCond(t, testTimeout, func() bool {
		return len(ch.eventsOf("reason.response")) == 1
	}, "retried round succeeds")

	// The LLM saw the 429: it remains part of the transcript after the retry.
	if !transcriptHasText(w.transcript.Render(), "429") {
		t.Fatal("transcript must surface the 429 to the LLM")
	}

	w.mu.Lock()
	attempts = w.rateLimitAttempts
	w.mu.Unlock()
	if attempts != 0 {
		t.Fatalf("rate-limit state must reset after success, got %d", attempts)
	}
}

// hasStopReason reports whether any broadcast reason.end carries stopReason.
func (m *testChannel) hasStopReason(stopReason string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.out {
		if e.Type == "reason.end" {
			if sr, _ := e.Payload["stop_reason"].(string); sr == stopReason {
				return true
			}
		}
	}
	return false
}

// transcriptHasText reports whether any message block in msgs contains sub.
func transcriptHasText(msgs []llm.Message, sub string) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if strings.Contains(b.Text, sub) {
				return true
			}
		}
	}
	return false
}
