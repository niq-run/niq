package inprocess

import (
	"context"
	"fmt"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// workerSide implements WorkerSideChannel for in-process transport.
//
// The worker creates its own WorkerSideChannel (对讲机), then calls
// Connect to push the paired BusSideChannel to the listener (射箭).
type workerSide struct {
	workerID  string
	listener  *InProcListener
	toBus     chan corebus.Request
	toWorker  chan event.Event
	connected bool
	closeOnce sync.Once
}

// NewWorkerSide creates a new in-process WorkerSideChannel.
// The worker holds this side; the paired BusSideChannel is delivered
// to the listener when Connect is called.
func NewWorkerSide(workerID string, listener *InProcListener) *workerSide {
	return &workerSide{
		workerID: workerID,
		listener: listener,
	}
}

func (w *workerSide) ID() string { return w.workerID }

// Connect establishes the connection to the bus (射箭).
//
// It creates the paired channels, constructs the BusSideChannel, and
// pushes it to the listener's Accept. The listener will receive it
// and (typically) Attach it to the engine.
//
// The endpoint parameter is ignored for in-process transport — the
// listener is already known from the constructor.
func (w *workerSide) Connect(ctx context.Context, endpoint string) error {
	toBus := make(chan corebus.Request, 64)
	toWorker := make(chan event.Event, 64)

	bs := &busSide{
		workerID: w.workerID,
		toWorker: toWorker,
		toBus:    toBus,
	}

	if err := w.listener.pushBusSide(ctx, bs); err != nil {
		close(toBus)
		close(toWorker)
		return err
	}

	w.toBus = toBus
	w.toWorker = toWorker
	w.connected = true
	return nil
}

func (w *workerSide) Send(ctx context.Context, evt event.Event, targets ...string) error {
	if !w.connected {
		return fmt.Errorf("inprocess: worker %s not connected", w.workerID)
	}
	if len(targets) == 0 {
		return fmt.Errorf("inprocess: Send requires at least one target")
	}
	req := corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{evt},
		Targets: targets,
	}
	select {
	case w.toBus <- req:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *workerSide) Broadcast(ctx context.Context, evt event.Event) error {
	if !w.connected {
		return fmt.Errorf("inprocess: worker %s not connected", w.workerID)
	}
	req := corebus.Request{
		Type:   corebus.RequestBroadcast,
		Events: []event.Event{evt},
	}
	select {
	case w.toBus <- req:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *workerSide) Receive(ctx context.Context) (<-chan event.Event, error) {
	if !w.connected {
		return nil, fmt.Errorf("inprocess: worker %s not connected", w.workerID)
	}
	return w.toWorker, nil
}

func (w *workerSide) Close() error {
	w.closeOnce.Do(func() {
		w.connected = false
		if w.toBus != nil {
			close(w.toBus)
		}
		if w.toWorker != nil {
			close(w.toWorker)
		}
	})
	return nil
}

// Compile-time check.
var _ corebus.WorkerSideChannel = (*workerSide)(nil)