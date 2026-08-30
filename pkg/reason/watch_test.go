package reason

import (
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
)

// TestInputDefaultTriggersReasoning verifies a default-mode input produces a
// completed reasoning round (a reason.end event).
func TestInputDefaultTriggersReasoning(t *testing.T) {
	prov := &staticProvider{msg: llm.Message{
		Role: llm.RoleAssistant, StopReason: "stop",
		Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}},
	}}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}

// TestInputAppendDoesNotInterruptReasoning verifies an append-mode input does
// NOT cancel an in-flight reasoning call (unlike default mode, which interrupts).
func TestInputAppendDoesNotInterruptReasoning(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	// Start a reasoning round and hold it in flight.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")

	// append-mode input must not interrupt the in-flight call.
	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "note", "input_mode": "append"})
	time.Sleep(100 * time.Millisecond)
	if ch.hasInterrupted() {
		t.Fatal("append-mode input must not interrupt in-flight reasoning")
	}

	// Release the provider; the round completes normally.
	close(prov.release)
	waitCond(t, 2*time.Second, func() bool {
		return len(ch.eventsOf("reason.end")) > 0
	}, "reason.end")
}
