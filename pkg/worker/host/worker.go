// Package host provides the HostWorker — the bus-facing worker that exposes
// spawn/suspend/resume tools for managing other workers' lifecycles.
//
// HostWorker is deliberately thin: it handles the bus protocol (tool.request
// events, replies) and forwards lifecycle operations to workerhost.WorkerService.
// It holds no knowledge of how specific worker types are built — that lives in
// the Builder registry the assembly layer registers on the WorkerService.
package host

import (
	"context"
	"fmt"
	"log"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/service/workerhost"
)

// Config holds the configuration for a HostWorker.
type Config struct {
	ID     string
	Bus    corebus.WorkerSideChannel // data-plane client
	Engine *workerhost.WorkerService
}

// HostWorker is a bus-facing worker that manages other worker lifecycles.
// It subscribes to tool.request and worker.discover, and exposes
// spawn/suspend/resume tools on the bus. Actual lifecycle operations are
// delegated to workerhost.WorkerService.
type HostWorker struct {
	worker.BaseWorker
	engine  *workerhost.WorkerService
	started bool
	cancel  context.CancelFunc
	mu      sync.Mutex
}

// New creates a HostWorker that delegates lifecycle operations to engine.
func New(cfg Config) *HostWorker {
	id := cfg.ID
	if id == "" {
		id = "host"
	}
	return &HostWorker{
		BaseWorker: worker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern(event.TypeToolRequest),
			event.NewPattern(event.TypeToolCancel),
			event.NewPattern(event.TypeWorkerDiscover),
		}, cfg.Bus),
		engine: cfg.Engine,
	}
}

// Start subscribes to the bus and begins watching for tool calls.
func (w *HostWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return fmt.Errorf("host: already started")
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancel = cancelFn

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.publishReady()

	w.started = true
	log.Println("[host] started")

	return nil
}

// Stop shuts down the host worker.
func (w *HostWorker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return nil
	}

	w.cancel()
	w.cancel = nil
	w.started = false
	log.Println("[host] stopped")

	return nil
}

func (w *HostWorker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *HostWorker) Restore(state []byte) error { return nil }

// ── Event loop ──

func (w *HostWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *HostWorker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		if evt.WorkerId != w.ID() {
			w.publishReady()
		}
	case event.TypeToolCancel:
		callID, _ := evt.Payload["call_id"].(string)
		log.Printf("[host %s] cancel requested for %s (best-effort)", w.ID(), callID)
	case event.TypeToolRequest:
		if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
			return
		}
		w.handleToolCall(evt)
	}
}
