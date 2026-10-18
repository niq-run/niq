package reason

import (
	"context"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestReadmeThroughProcessDoesNotDeadlock guards the lock discipline of the
// readme extensions: process() holds w.mu for the whole dispatch, so a handler
// that takes it again self-deadlocks the event loop — the worker then stops
// processing events for good (it never logs the next event and never reasons
// again). Both halves must therefore use the *Locked accessors.
func TestReadmeThroughProcessDoesNotDeadlock(t *testing.T) {
	cases := []struct {
		name string
		evt  func() event.Event
	}{
		{"read", func() event.Event {
			e := event.New(event.TypeWorkerReadme, "r1", nil)
			e.RequestId = "req-read"
			return e
		}},
		{"update", func() event.Event {
			e := event.New(TypeReadmeUpdate, "r1", map[string]any{"readme": " new role "})
			e.RequestId = "req-upd"
			return e
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewBaseReasonWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: newTestChannel(), Readme: "seed text"})
			done := make(chan struct{})
			go func() {
				w.process(context.Background(), tc.evt())
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatalf("process(worker.readme %s) deadlocked: the handler re-locked w.mu, which process already holds", tc.name)
			}
		})
	}
}
