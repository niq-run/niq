package baseworker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestDurableChangeDeliveredOffCallerGoroutine is the contract the whole
// mechanism exists for: persisting means Snapshot(), which takes the lock the
// notifier holds. So the callback must never run on the caller's goroutine.
func TestDurableChangeDeliveredOffCallerGoroutine(t *testing.T) {
	w := NewBaseWorker("w", nil, nil)

	// Stand in for the embedding worker's lock: the notifier holds it while
	// raising the signal, and the callback takes it — the same pairing that
	// would deadlock if the callback ran inline.
	var mu sync.Mutex
	var called bool
	w.SetOnDurableChange(func() {
		mu.Lock()
		defer mu.Unlock()
		called = true
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartDurableLoop(ctx)

	mu.Lock()
	w.NotifyDurableChange()
	mu.Unlock() // released immediately: the callback has not run yet

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := called
		mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("callback never ran")
}

// TestDurableChangeCoalesced verifies a burst of changes collapses into one
// pending notification: the channel is a "something changed" flag, not a
// queue. The single callback that follows still observes the latest state,
// because it runs after all of them.
func TestDurableChangeCoalesced(t *testing.T) {
	w := NewBaseWorker("w", nil, nil)

	var calls atomic.Int32
	w.SetOnDurableChange(func() { calls.Add(1) })

	// Three changes arrive before anything is delivered (the loop is not
	// running yet, so the notifications just pile up).
	w.NotifyDurableChange()
	w.NotifyDurableChange()
	w.NotifyDurableChange()
	if pending := len(w.durability.ch); pending != 1 {
		t.Fatalf("pending notifications = %d, want 1 (a burst coalesces)", pending)
	}

	// Delivery drains the single flag: one callback for the whole burst, and
	// the snapshot it takes lands after the last change.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartDurableLoop(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && calls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("callback calls = %d, want 1 for a coalesced burst", got)
	}
	if pending := len(w.durability.ch); pending != 0 {
		t.Fatalf("pending notifications = %d, want 0 after delivery", pending)
	}
}

// TestDurableChangeOffByDefault verifies a worker with no callback installed
// neither panics nor starts a goroutine — signalling is opt-in.
func TestDurableChangeOffByDefault(t *testing.T) {
	w := NewBaseWorker("w", nil, nil)
	w.NotifyDurableChange() // no callback: must be a no-op
	w.StartDurableLoop(context.Background())
	w.NotifyDurableChange()
	if len(w.durability.ch) != 0 {
		t.Fatal("notification recorded with no callback installed")
	}
}

// TestDurableChangeCopiesShareSignalling verifies the pointer-behind-value
// trick: a BaseWorker copied into an embedding struct shares one channel, so
// registering on either the original or the copy is the same registration.
func TestDurableChangeCopiesShareSignalling(t *testing.T) {
	base := NewBaseWorker("w", nil, nil)
	copyOf := base // what an embedding literal does

	copyOf.SetOnDurableChange(func() {})
	base.NotifyDurableChange()
	if len(base.durability.ch) != 1 {
		t.Fatal("notification registered via a copy was not seen by the original")
	}
}
