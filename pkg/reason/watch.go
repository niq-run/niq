// Event loop: the single goroutine that pulls events off the bus. Dispatch is
// in process.go (the "what to do with an event" half); here is the loop and
// the decision gate that decides after each event whether to trigger
// reasoning.
package reason

import (
	"context"

	"github.com/niq-run/niq/core/event"
)

// watch is the single event loop goroutine. It blocks on busCh waiting
// for events, calls process() to handle them, and then calls tryReason()
// which starts reasoning when needReason is set and no reasoning is running.
func (w *BaseReasonWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(ctx, evt)
		case <-ctx.Done():
			return
		}
		w.tryReason(ctx)
	}
}

// tryReason is the decision gate. It is called after every process() in the
// watch loop, and at the end of every reason(). If needReason is set and no
// reasoning is running, it spawns a reasoning round on its own goroutine so
// the watch event loop stays responsive while the LLM call is in flight.
func (w *BaseReasonWorker) tryReason(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.needReason || w.isReasoning {
		return
	}

	w.isReasoning = true
	w.needReason = false

	go w.reason(ctx)
}
