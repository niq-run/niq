package iface

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// Config describes an interface worker.
type Config struct {
	// ID is the worker's identity on the bus (required).
	ID string
	// Type is the identity's type label, announced to peers through
	// worker.ready and discover (e.g. "niq", "interface").
	Type string
	// Bus is the worker-side channel, provided by the assembly layer exactly like
	// any other managed worker.
	Bus corebus.WorkerSideChannel
	// Backends are the outward-facing surfaces this worker bridges to. At least
	// one is normally present; a worker with none starts and simply serves its
	// tools.
	Backends []Backend
	// Publishes is the presence contract announced via worker.ready: the event
	// types this worker emits, described for peers and the directory roster.
	Publishes []map[string]any
	// State is the optional durable state owned by a backend. Nil means this
	// worker keeps nothing across a restart.
	State Stateful
	// RequestTimeout overrides DefaultRequestTimeout.
	RequestTimeout time.Duration
}

// Worker is the interface worker: a bus participant that bridges events to its
// backends. It embeds baseworker for the tool/extension plumbing, presence and
// readme/help conventions, and adds the two things those backends need — the
// outbound verbs and inbound routing.
type Worker struct {
	baseworker.BaseWorker

	typ      string
	backends []Backend
	pubs     []map[string]any
	state    Stateful
	timeout  time.Duration

	h *handler

	mu      sync.Mutex
	cancel  context.CancelFunc
	started bool
}

// New builds an interface worker. It does not touch the bus: Start does.
func New(cfg Config) *Worker {
	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRequestTimeout
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(cfg.ID, cfg.Bus),
		typ:        cfg.Type,
		backends:   cfg.Backends,
		pubs:       cfg.Publishes,
		state:      cfg.State,
		timeout:    timeout,
	}
	w.h = newHandler(cfg.ID, cfg.Bus, timeout)
	return w
}

// Type is the identity's type label.
func (w *Worker) Type() string { return w.typ }

// Start hands every backend its handler, then joins the bus: announce presence
// and serve inbound events until Stop or the context is cancelled.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("iface: %s already started", w.ID())
	}

	// Injection first: a backend must be able to reach inward before anything
	// arrives from outside.
	for _, b := range w.backends {
		if b == nil {
			continue
		}
		b.SetHandler(w.h)
	}

	busCh, err := w.Channel.Receive(ctx)
	if err != nil {
		return fmt.Errorf("iface: %s receive: %w", w.ID(), err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.started = true

	w.AnnounceReady(w.typ, w.pubs)
	disc := event.New(event.TypeWorkerDiscover, w.ID(), nil)
	disc.Transient = true // presence: live-only, not durable history
	_ = w.Channel.Broadcast(runCtx, disc)

	w.StartDurableLoop(runCtx)
	go w.serve(runCtx, busCh)
	return nil
}

// serve is the worker's event loop: replies resolve pending requests, tools go
// to the extension registry, everything else fans out to interested backends.
// A single loop keeps delivery ordered and lets a slow backend be isolated (it
// has its own queue) instead of stalling the bus.
func (w *Worker) serve(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt, ok := <-busCh:
			if !ok {
				return
			}
			w.dispatch(ctx, evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) dispatch(ctx context.Context, evt event.Event) {
	// A reply to something this worker asked: resolve it before anything else, so
	// a caller's Request returns as soon as the answer arrives.
	if w.h.resolve(evt) {
		// A reply is also a normal inbound event: a backend watching the
		// conversation may want to render it.
		w.h.publish(evt)
		return
	}

	switch evt.Type {
	case event.TypeWorkerDiscover:
		// Answer a discover directly rather than through the extension registry:
		// the question can arrive after the asker's roster changed, and answering
		// it is the worker's own presence contract.
		w.AnswerDiscover(evt, w.typ, w.pubs, false)
		return
	}
	if w.DispatchExtension(evt) {
		return
	}
	// Not a tool this worker serves: it is a message for the outside world.
	w.h.publish(evt)
}

// Stop leaves the bus and closes every subscription. Backends are not told to
// stop: they were injected by the assembly, which owns their lifetime.
func (w *Worker) Stop() error {
	w.mu.Lock()
	cancel := w.cancel
	w.cancel = nil
	w.started = false
	w.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	w.h.close()
	if w.Channel != nil {
		_ = w.Channel.Close()
	}
	return nil
}

// Snapshot returns the backend's durable state, or nothing when the worker keeps
// no state. The worker itself has none: everything worth persisting belongs to
// the thing behind it.
func (w *Worker) Snapshot() ([]byte, error) {
	if w.state == nil {
		return nil, nil
	}
	return w.state.Snapshot()
}

// Restore replays the backend's durable state.
func (w *Worker) Restore(state []byte) error {
	if w.state == nil || len(state) == 0 {
		return nil
	}
	return w.state.Restore(state)
}

// logf is the package's single logging seam, so tests stay quiet.
func logf(format string, args ...any) {
	log.Printf("[iface] "+format, args...)
}
