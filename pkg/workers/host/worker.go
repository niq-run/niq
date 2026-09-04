// Package host provides the HostWorker — the bus-facing worker that exposes
// spawn/suspend/resume tools for managing other workers' lifecycles.
//
// HostWorker is deliberately thin: it handles the bus protocol (tool
// invocation events, replies) and forwards lifecycle operations to
// workerhost.WorkerService.
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
	"github.com/niq-run/niq/pkg/baseworker"
	"github.com/niq-run/niq/pkg/services/workerhost"
)

// Config holds the configuration for a HostWorker.
type Config struct {
	ID     string
	Bus    corebus.WorkerSideChannel // data-plane client
	Engine *workerhost.WorkerService
}

// HostWorker is a bus-facing worker that manages other worker lifecycles.
// It subscribes to its tool events and worker.discover, and exposes
// spawn/suspend/resume tools on the bus. Actual lifecycle operations are
// delegated to workerhost.WorkerService.
type HostWorker struct {
	baseworker.BaseWorker
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
	w := &HostWorker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		engine: cfg.Engine,
	}
	w.registerExtensions()
	return w
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
	w.AnnounceReady("host", nil)

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
			w.AnnounceReady("host", nil)
		}
	case event.TypeRequestCancel:
		callID := evt.RequestId
		log.Printf("[host %s] cancel requested for %s (best-effort)", w.ID(), callID)
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[host %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}
