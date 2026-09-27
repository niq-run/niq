package baseworker

import "context"

// Durable-change signalling: how a worker tells the layer that owns its
// persistence "state worth keeping changed — snapshot me".
//
// The mechanism exists because of one constraint: the change is usually
// discovered while the worker holds its own lock (an event handler running
// inside the event loop), while persisting means Snapshot(), which takes that
// same lock. Calling the callback inline would deadlock. So the notification
// is a signal, not an action: it is recorded here and delivered later, on a
// goroutine of the worker's own, which is free to take the lock.
//
// Like the extension registry, the state lives behind a pointer so BaseWorker
// stays copyable by value and copies share one signalling channel.
//
// This is generic bus machinery: it knows nothing about what a worker
// persists, or whether it persists anything at all.
type durability struct {
	onChange func()
	ch       chan struct{}
}

// SetOnDurableChange installs the callback invoked after the worker changes
// state that must survive a restart. It must be called before
// StartDurableLoop; nil (or never calling this) leaves signalling off, and
// then NotifyDurableChange is a no-op and no goroutine is started.
//
// The callback is invoked on the worker's durable goroutine, never on the
// caller's, so it may take locks the caller holds (notably the lock
// Snapshot() needs). It should return promptly; it is called at most once per
// pending change, not once per change (see NotifyDurableChange).
func (w *BaseWorker) SetOnDurableChange(fn func()) {
	w.durability.onChange = fn
}

// NotifyDurableChange records that this worker changed state worth persisting
// and schedules the callback. It does not block and never calls back inline,
// so it is safe to call while holding the embedding worker's lock.
//
// Notifications coalesce: when one is already pending, the new one is dropped.
// That is safe — the pending callback has not run yet, so the snapshot it
// takes will already include this change. The effect is that a burst of
// changes persists once, and because delivery is serialized on a single
// goroutine, only the newest snapshot can be the last one written.
func (w *BaseWorker) NotifyDurableChange() {
	if w.durability == nil || w.durability.onChange == nil {
		return
	}
	select {
	case w.durability.ch <- struct{}{}:
	default:
	}
}

// StartDurableLoop starts the goroutine that delivers durable-change
// notifications, and returns immediately. It is a no-op when no callback was
// installed. The embedding worker calls it from its own Start with the same
// context that governs its event loop, so both stop together.
func (w *BaseWorker) StartDurableLoop(ctx context.Context) {
	if w.durability == nil || w.durability.onChange == nil {
		return
	}
	go func() {
		for {
			select {
			case <-w.durability.ch:
				w.durability.onChange()
			case <-ctx.Done():
				return
			}
		}
	}()
}
