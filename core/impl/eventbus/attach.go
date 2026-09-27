package eventbus

import (
	"context"
	"log"

	corebus "github.com/niq-run/niq/core/itfs/bus"
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
		engine.DisconnectChannel(workerID, ch)
		return
	}
	for req := range reqCh {
		// Defensive: a panic while routing one request must not take down the
		// whole project process (a goroutine panic is fatal in Go). Recover it,
		// log it, and keep serving this worker. Routing is the biggest surface
		// (Send/broadcast/persist), so guard here rather than across every
		// goroutine.
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[eventbus] watch %s: recovered panic in HandleRequest: %v", workerID, r)
				}
			}()
			engine.HandleRequest(ctx, req, workerID)
		}()
	}
	// Guard the teardown too: DisconnectChannel fans out a worker.gone broadcast
	// (broadcastGone -> Send on peer channels), which can race another worker's
	// teardown. A panic here must not escape the goroutine.
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[eventbus] watch %s: recovered panic in DisconnectChannel: %v", workerID, r)
			}
		}()
		engine.DisconnectChannel(workerID, ch)
	}()
	log.Printf("[eventbus] watch %s: channel closed, disconnected", workerID)
}
