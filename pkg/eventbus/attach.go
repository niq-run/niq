package eventbus

import (
	"context"
	"log"

	corebus "github.com/niq-run/niq/core/bus"
)

// Attach connects a BusSideChannel to the Engine and starts watching it.
//
// Attach is the "接线员" — it performs three actions in sequence:
//  1. Connect: register the channel with the engine
//  2. Watch: start a goroutine that reads Requests from the channel
//  3. Disconnect: when the channel closes, clean up
//
// The goroutine exits when ctx is cancelled or the channel's Receive
// stream ends (worker disconnected).
func Attach(ctx context.Context, engine *Engine, workerID string, ch corebus.BusSideChannel) {
	if err := engine.Connect(workerID, ch); err != nil {
		log.Printf("[eventbus] attach %s: %v", workerID, err)
		return
	}
	go watch(ctx, engine, workerID, ch)
}

// watch reads Requests from the channel and forwards them to the engine.
// It exits when the channel closes, then disconnects the worker.
func watch(ctx context.Context, engine *Engine, workerID string, ch corebus.BusSideChannel) {
	reqCh, err := ch.Receive(ctx)
	if err != nil {
		log.Printf("[eventbus] watch %s: receive failed: %v", workerID, err)
		engine.Disconnect(workerID)
		return
	}
	for req := range reqCh {
		engine.HandleRequest(ctx, req, workerID)
	}
	engine.Disconnect(workerID)
	log.Printf("[eventbus] watch %s: channel closed, disconnected", workerID)
}