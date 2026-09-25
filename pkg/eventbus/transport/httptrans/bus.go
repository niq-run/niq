package httptrans

import (
	"context"
	"sync"
	"sync/atomic"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// busSide implements BusSideChannel over HTTP transport.
//
// Events are pushed to the worker via SSE. Requests from the worker
// arrive via POST /publish and are placed on the toBus channel.
//
// mu serializes Send and Close so no Send can ever target a closed channel.
// Go panics on "send on closed channel", and a worker disconnect (SSE teardown
// calling Close) races with a concurrent broadcast holding a reference to the
// channel — without this guard that race killed the whole project process.
type busSide struct {
	workerID string
	toWorker chan event.Event      // SSE goroutine reads from here
	toBus    chan corebus.Request  // POST /publish writes to here

	mu     sync.Mutex // serializes Send and Close; guards `closed`
	closed bool
}

// dropped counts outbound deliveries dropped because the worker's buffer was
// full (its SSE consumer stalled). Package-level so it survives across
// channels; read via DroppedCounter.
var droppedCount atomic.Uint64

// DroppedCounter reports how many deliveries were dropped for a full outbound
// buffer, a per-process observability signal for a slow/stalled consumer.
func DroppedCounter() uint64 { return droppedCount.Load() }

func (ch *busSide) ID() string       { return ch.workerID }
func (ch *busSide) WorkerID() string { return ch.workerID }

func (ch *busSide) Send(ctx context.Context, evt event.Event) error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.closed {
		return corebus.ErrClosed
	}
	select {
	case ch.toWorker <- evt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Outbound buffer full: the worker's SSE consumer is stalled. Blocking
		// here would hold ch.mu and (for a broadcast) the engine read lock,
		// wedging the whole bus behind one slow worker. Drop instead, counting
		// the loss so it stays observable.
		droppedCount.Add(1)
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