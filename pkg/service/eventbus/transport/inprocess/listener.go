// Package inprocess provides an in-process transport implementation for the event bus.
//
// It implements the "球和箭" model:
//   - Worker creates its own WorkerSideChannel (对讲机)
//   - Worker calls Connect (射箭) — pushes a BusSideChannel to the listener
//   - Listener calls Accept (接箭) — receives the BusSideChannel
//   - Attach (插线) — connects the BusSideChannel to the engine
package inprocess

import (
	"context"

	corebus "github.com/niq-run/niq/core/bus"
)

// InProcListener is an in-process Listener — the "守塔人".
//
// It waits for workers to connect (via their WorkerSideChannel.Connect)
// and returns the corresponding BusSideChannel through Accept.
//
// Usage:
//
//	listener := inprocess.NewInProcListener()
//	go func() {
//	    for {
//	        ch, _ := listener.Accept(ctx)
//	        eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
//	    }
//	}()
//	// Worker side:
//	workerCh := inprocess.NewWorkerSide("worker-1", listener)
//	workerCh.Connect(ctx, "inproc://niq")
type InProcListener struct {
	acceptCh chan *busSide
}

// NewInProcListener creates a new in-process listener.
func NewInProcListener() *InProcListener {
	return &InProcListener{
		acceptCh: make(chan *busSide),
	}
}

// Accept blocks until a worker connects, then returns the BusSideChannel.
// This is called by the bus side (the "守塔人").
func (l *InProcListener) Accept(ctx context.Context) (corebus.BusSideChannel, error) {
	select {
	case bs := <-l.acceptCh:
		return bs, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pushBusSide delivers a BusSideChannel from a connecting worker.
// Used internally by workerSide.Connect.
func (l *InProcListener) pushBusSide(ctx context.Context, bs *busSide) error {
	select {
	case l.acceptCh <- bs:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}