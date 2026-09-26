package inprocess

import (
	"context"
	"sync"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// TestSendAfterCloseDoesNotPanic verifies the in-process busSide never panics
// with "send on closed channel" — the same defect the HTTP transport had,
// fixed here with the identical mutex guard.
func TestSendAfterCloseDoesNotPanic(t *testing.T) {
	ch := &busSide{
		toWorker: make(chan event.Event, 8),
		toBus:    make(chan corebus.Request, 8),
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := ch.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := ch.Send(context.Background(), event.New("x", "w", nil)); err != corebus.ErrClosed {
		t.Fatalf("send after close = %v, want ErrClosed", err)
	}
}

// TestConcurrentSendCloseDoesNotPanic races Send against Close (the suspend /
// disconnect / broadcast-storm crash window).
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
			_ = ch.Send(context.Background(), event.New("x", "w", map[string]any{"i": i}))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = ch.Close()
	}()
	wg.Wait()
}