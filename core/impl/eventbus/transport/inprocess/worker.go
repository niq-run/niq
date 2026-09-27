package inprocess

import (
	"context"
	"fmt"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// workerSide implements WorkerSideChannel for in-process transport.
//
// The worker creates its own WorkerSideChannel (对讲机), then calls
// Connect to push the paired BusSideChannel to the listener (射箭).
//
// The channel pair (toBus/toWorker) has exactly one owner: busSide.
// workerSide only operates on the channels through Send/Broadcast/Receive
// and never closes them itself — Close delegates to busSide.Close, whose
// mu+closed guard is the single idempotent close path. Two independent
// closers on the same pair (a plain close here plus the guarded busSide
// close) raced and caused "send on closed channel"/"close of closed
// channel" panics during host suspend.
type workerSide struct {
	workerID  string
	listener  *InProcListener
	bs        *busSide // owns the channel pair; single close path
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
		// Not published: the only reference is this function, so closing the
		// channels directly is safe — busSide was never handed off.
		close(toBus)
		close(toWorker)
		return err
	}

	w.bs = bs
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
		// Delegate to the single owner of the channel pair so close is
		// idempotent and no stray Send (peer broadcast, presence gone event)
		// can hit a closed channel between the two closers.
		if w.bs != nil {
			_ = w.bs.Close()
		}
	})
	return nil
}

// Compile-time check.
var _ corebus.WorkerSideChannel = (*workerSide)(nil)
