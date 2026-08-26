// Package hiw provides the Human Interface Worker — a minimal worker that
// publishes user input to the bus as the webui-hiw identity.
//
// HIW is the human's voice in the swarm. It does not manage UI lifecycle,
// stream events, or serve HTTP — those are the swarm's responsibility.
package hiw

import (
	"context"
	"fmt"
	"sync"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// Worker is the Human Interface Worker.
// It publishes worker.input events on behalf of the human user.
type Worker struct {
	worker.BaseWorker
	started  bool
	cancelCh chan struct{}
	mu       sync.Mutex
}

// Config holds the configuration for a HIW.
type Config struct {
	ID  string // worker ID, defaults to "webui-hiw"
	Bus corebus.WorkerSideChannel
}

// New creates a new HIW worker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "webui-hiw"
	}
	return &Worker{
		BaseWorker: worker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern("*"),
		}, cfg.Bus),
		cancelCh: make(chan struct{}),
	}
}

// Start starts the HIW worker's event loop.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("hiw: already started")
	}

	busCh, _ := w.Channel.Receive(context.Background())

	// Announce HIW's presence on the bus.
	w.publishReady()
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), nil))

	ch := w.cancelCh
	go func() {
		for {
			select {
			case <-busCh:
				// Drain incoming events. HIW does not react to them; the
				// swarm's EventLog handles streaming.
			case <-ch:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	w.started = true
	return nil
}

// Stop stops the HIW worker.
func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	close(w.cancelCh)
	w.started = false
	return nil
}

// SendInput publishes a user message to the bus as a worker.input event.
// If target is non-empty, the message is directed to that worker.
// mode controls input handling ("default", "append", "interrupt").
func (w *Worker) SendInput(ctx context.Context, text string, target string, mode string) error {
	payload := map[string]any{"text": text}
	if mode != "" && mode != "default" {
		payload["input_mode"] = mode
	}
	evt := event.New(event.TypeWorkerInput, w.ID(), payload)
	evt.TraceID = evt.ID
	if target != "" {
		return w.Channel.Send(context.Background(), evt, target)
	}
	return w.Channel.Broadcast(context.Background(), evt)
}

// publishReady announces HIW on the bus.
func (w *Worker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "hiw",
		"publishes": []map[string]any{
			{"type": "worker.input", "description": "User input event"},
		},
	}))
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }
