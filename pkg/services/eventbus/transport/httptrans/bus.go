// Package httptrans provides an HTTP transport implementation for the event bus.
//
// It uses SSE (Server-Sent Events) for bus-to-worker event delivery and
// HTTP POST for worker-to-bus requests. This allows workers in different
// processes or on different machines to participate in the bus.
package httptrans

import (
	"context"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// busSide implements BusSideChannel over HTTP transport.
//
// Events are pushed to the worker via SSE. Requests from the worker
// arrive via POST /publish and are placed on the toBus channel.
type busSide struct {
	workerID  string
	toWorker  chan event.Event // SSE goroutine reads from here
	toBus     chan corebus.Request // POST /publish writes to here
	closeOnce sync.Once
}

func (ch *busSide) ID() string       { return ch.workerID }
func (ch *busSide) WorkerID() string { return ch.workerID }

func (ch *busSide) Send(ctx context.Context, evt event.Event) error {
	select {
	case ch.toWorker <- evt:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (ch *busSide) Receive(ctx context.Context) (<-chan corebus.Request, error) {
	return ch.toBus, nil
}

func (ch *busSide) Close() error {
	ch.closeOnce.Do(func() {
		close(ch.toWorker)
		close(ch.toBus)
	})
	return nil
}

// Compile-time check.
var _ corebus.BusSideChannel = (*busSide)(nil)