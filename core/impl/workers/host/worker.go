// Package host provides the HostWorker — the bus-facing worker that owns other
// workers' lifecycles, plus the generic worker.readme and worker.help
// capabilities.
//
// HostWorker is deliberately thin: it handles the bus protocol (tool
// invocation events, replies, destroy approvals) and forwards lifecycle
// operations to a worker.Lifecycle backend. It holds no knowledge of how
// specific worker types are built, where declarations live, or how a
// third-party process is launched — that is the backend the assembly layer
// supplies.
//
// Lifecycle operations are host's ONLY job. The fleet roster (who exists, with
// what tools) deliberately lives on the separate directory worker — a reason
// worker must not be led to think host can enumerate capabilities, nor that
// host's knowledge is limited to workers it spawned.
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/niq-run/niq/core/impl/baseworker"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// Config holds the configuration for a HostWorker.
type Config struct {
	ID     string
	Bus    corebus.WorkerSideChannel // data-plane client
	Engine worker.Lifecycle          // lifecycle backend for every placement
	// Params is the declaration's construction params, read only for the
	// optional readme/readme_append guide customization and the destroy
	// approval wiring (see RegisterReadme and destroyApprover).
	Params map[string]any
}

// HostWorker is a bus-facing worker that manages other worker lifecycles.
// It subscribes to its tool events, worker.discover and approval.decision, and
// exposes spawn/suspend/resume/restart/destroy on the bus. Actual lifecycle
// operations are delegated to the worker.Lifecycle backend.
type HostWorker struct {
	baseworker.BaseWorker
	engine  worker.Lifecycle
	params  map[string]any
	started bool
	cancel  context.CancelFunc
	mu      sync.Mutex

	// pendingDestroy holds destroy operations parked on an approval decision,
	// keyed by the approval request id (which the decision echoes back).
	pendingMu      sync.Mutex
	pendingDestroy map[string]*pendingDestroy
}

// New creates a HostWorker that delegates lifecycle operations to engine.
func New(cfg Config) *HostWorker {
	id := cfg.ID
	if id == "" {
		id = "host"
	}
	w := &HostWorker{
		BaseWorker:     baseworker.NewBaseWorker(id, cfg.Bus),
		engine:         cfg.Engine,
		params:         cfg.Params,
		pendingDestroy: map[string]*pendingDestroy{},
	}
	w.registerExtensions()
	w.registerReadme()
	w.registerHelp()
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

// Snapshot captures the destroy operations parked on an approval, so a restart
// does not silently drop an approved-but-not-yet-applied removal.
func (w *HostWorker) Snapshot() ([]byte, error) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	if len(w.pendingDestroy) == 0 {
		return nil, nil
	}
	state := hostState{PendingDestroy: make([]pendingDestroy, 0, len(w.pendingDestroy))}
	for _, p := range w.pendingDestroy {
		state.PendingDestroy = append(state.PendingDestroy, *p)
	}
	return json.Marshal(state)
}

// Restore rehydrates the parked destroy set from a Snapshot, before Start.
func (w *HostWorker) Restore(state []byte) error {
	if len(state) == 0 {
		return nil
	}
	var s hostState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("host restore: %w", err)
	}
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	w.pendingDestroy = make(map[string]*pendingDestroy, len(s.PendingDestroy))
	for i := range s.PendingDestroy {
		p := s.PendingDestroy[i]
		w.pendingDestroy[p.ApprovalID] = &p
	}
	return nil
}

// hostState is host's durable state: the destroys waiting on an approval.
type hostState struct {
	PendingDestroy []pendingDestroy `json:"pending_destroy,omitempty"`
}

// ── Event loop ──

func (w *HostWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-busCh:
			if !ok {
				return
			}
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *HostWorker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		// Answer the discoverer directly (no broadcast re-announce — a
		// discover storm must not fan O(N²) ready broadcasts). The
		// directory worker owns the roster; host only says what it serves.
		w.AnswerDiscover(evt, "host", nil, false)
	case event.TypeRequestCancel:
		callID := evt.RequestId
		log.Printf("[host %s] cancel requested for %s (best-effort)", w.ID(), callID)
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[host %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}
