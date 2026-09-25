package httptrans

import (
	"context"
	"sync"
	"testing"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// TestSendAfterCloseDoesNotPanic verifies that delivering to a closed busSide
// returns corebus.ErrClosed instead of panicking ("send on closed channel" —
// the crash that killed a project process). The mutex guard in busSide makes
// Send and Close atomic.
func TestSendAfterCloseDoesNotPanic(t *testing.T) {
	ch := &busSide{
		toWorker: make(chan event.Event, 8),
		toBus:    make(chan corebus.Request, 8),
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	// A second Close is a no-op (not a double-close panic).
	if err := ch.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	err := ch.Send(context.Background(), event.New("x", "w", nil))
	if err != corebus.ErrClosed {
		t.Fatalf("send after close = %v, want ErrClosed", err)
	}
}

// TestConcurrentSendCloseDoesNotPanic races Send against Close to exercise the
// crash window the fix targets. A regression (unguarded send-on-closed) would
// panic and fail the test process.
func TestConcurrentSendCloseDoesNotPanic(t *testing.T) {
	ch := &busSide{
		toWorker: make(chan event.Event, 8),
		toBus:    make(chan corebus.Request, 8),
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			//nolint:errcheck // either send succeeds or returns a guarded error
			_ = ch.Send(context.Background(), event.New("x", "w", map[string]any{"i": i}))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = ch.Close()
	}()
	wg.Wait()
	// If any Send ran after Close, it got ErrClosed (handled), not a panic.
}