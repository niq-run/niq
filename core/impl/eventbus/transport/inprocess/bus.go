package inprocess

import (
	"context"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// busSide implements BusSideChannel for in-process transport.
//
// mu serializes Send and Close so no Send can ever target a closed channel.
// Host-managed workers suspend/resume (stop + reconnect) and broadcast storms
// race a worker's teardown, so without this guard a Send against a closed
// toWorker panicked ("send on closed channel") and killed the process — the
// same defect the HTTP transport had, fixed here with the identical shape.
type busSide struct {
	workerID string
	toWorker chan event.Event
	toBus    chan corebus.Request

	mu     sync.Mutex // serializes Send and Close; guards `closed`
	closed bool
}

func (ch *busSide) ID() string       { return ch.workerID }
func (ch *busSide) WorkerID() string { return ch.workerID }

func (ch *busSide) Send(ctx context.Context, evt event.Event) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.closed {
		return corebus.ErrClosed
	}
	// Non-blocking: holding ch.mu across the send (to keep it atomic with
	// Close) must never block — otherwise a stalled consumer would wedge Close
	// too. If the buffer is full we drop rather than block; a full buffer means
	// the worker is not draining (suspended/dead), so the event is going nowhere
	// anyway.
	select {
	case ch.toWorker <- evt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return corebus.ErrDropped
	}
}

func (ch *busSide) Receive(context.Context) (<-chan corebus.Request, error) {
	return ch.toBus, nil
}

func (ch *busSide) Close() error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.closed {
		return nil
	}
	ch.closed = true
	close(ch.toWorker)
	close(ch.toBus)
	return nil
}

// Compile-time check.
var _ corebus.BusSideChannel = (*busSide)(nil)