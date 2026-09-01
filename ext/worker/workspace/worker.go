package workspace

import (
	"context"
	"fmt"
	"log"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/baseworker"
)

// Mode controls which tools the WorkspaceWorker registers.
type Mode int

const (
	ModeFull     Mode = iota // all tools
	ModeReadOnly             // read, ls, grep, find only
	ModeSafe                 // read, write, edit, ls, grep, find (no bash)
)

// Config holds the configuration for a WorkspaceWorker.
type Config struct {
	ID      string
	Bus     corebus.WorkerSideChannel
	Backend any
	Mode    Mode
}

// WorkspaceWorker is a worker.ManagedWorker that provides file, command,
// and directory operations. It delegates I/O to a shared Backend and
// controls tool availability through its Mode.
type WorkspaceWorker struct {
	baseworker.BaseWorker
	backend   any
	mode      Mode
	handlers  map[string]worker.ToolFunc
	started   bool
	cancelRun context.CancelFunc
	mu        sync.Mutex
}

func New(cfg Config) *WorkspaceWorker {
	// Workspace tools are dynamic (mode/backend dependent), so the worker
	// subscribes to everything and routes via the extension registry.
	subs := []event.EventPattern{
		event.NewPattern("*"),
	}
	return &WorkspaceWorker{
		BaseWorker: baseworker.NewBaseWorker(cfg.ID, subs, cfg.Bus),
		backend:    cfg.Backend,
		mode:       cfg.Mode,
	}
}

// ── Lifecycle ──

func (w *WorkspaceWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("workspace %s: already started", w.ID())
	}
	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancelRun = cancelFn

	w.buildHandlers()
	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.AnnounceReady("workspace", nil)
	w.started = true
	return nil
}

func (w *WorkspaceWorker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false
	return nil
}

func (w *WorkspaceWorker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *WorkspaceWorker) Restore(state []byte) error { return nil }

// ── Event loop ──

func (w *WorkspaceWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(ctx, evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *WorkspaceWorker) process(ctx context.Context, evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		w.AnnounceReady("workspace", nil)
	case event.TypeRequestCancel:
		callID := evt.RequestId
		log.Printf("[workspace %s] cancel requested for %s (best-effort)", w.ID(), callID)
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[workspace %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}
