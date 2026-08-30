package inprocess

import (
	"context"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// busSide implements BusSideChannel for in-process transport.
type busSide struct {
	workerID  string
	toWorker  chan event.Event
	toBus     chan corebus.Request
	closeOnce sync.Once
}

func (ch *busSide) ID() string        { return ch.workerID }
func (ch *busSide) WorkerID() string  { return ch.workerID }

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