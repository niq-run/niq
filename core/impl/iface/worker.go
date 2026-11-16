package iface

import (
	"context"
	"encoding/json"
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
	//
	// OnDurableChange, when set, is called whenever the WORKER's own state changed
	// (the approval inbox). It is the same signal other workers raise: the layer
	// that owns the snapshot store decides when to write it.
	OnDurableChange func()
	Backends        []Backend
	// Publishes is the presence contract announced via worker.ready: the event
	// types this worker emits, described for peers and the directory roster.
	Publishes []map[string]any
	// Approvals serves the human approval inbox (approval.request in, a decision
	// out). It is opt-in because serving approvals is a DECLARATION: the worker
	// advertises approval.request in its watch contract and must be granted it.
	// Backends reach the inbox by asserting their Handler to Inbox.
	Approvals bool
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
	state    Stateful // first backend that is Stateful, resolved at construction
	timeout  time.Duration

	// h is the worker's own handler (used internally); handle is what backends
	// receive — the same thing, or a wrapper that also exposes the inbox.
	h      *handler
	handle Handler
	inbox  *approvals

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
	var state Stateful
	for _, b := range cfg.Backends {
		if s, ok := b.(Stateful); ok && state == nil {
			state = s
		}
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(cfg.ID, cfg.Bus),
		typ:        cfg.Type,
		backends:   cfg.Backends,
		pubs:       cfg.Publishes,
		state:      state,
		timeout:    timeout,
	}
	w.h = newHandler(cfg.ID, cfg.Bus, timeout)
	w.handle = w.h
	if cfg.Approvals {
		// The inbox is a capability of the HANDLE, not a second injection point:
		// a backend gets one object and asserts the optional interfaces it needs.
		w.inbox = newApprovals(w.h)
		w.handle = &inboxHandle{Handler: w.h, approvals: w.inbox}
		w.inbox.registerOn(w)
		if cfg.OnDurableChange != nil {
			w.inbox.OnChange(cfg.OnDurableChange)
		}
	}
	return w
}

// Type is the identity's type label.
func (w *Worker) Type() string { return w.typ }

// Inbox returns the approval inbox, or nil when Config.Approvals is off.
func (w *Worker) Inbox() Inbox {
	if w.inbox == nil {
		return nil
	}
	return w.inbox
}

// inboxHandle is what a backend receives when the worker serves approvals: the
// handler plus the inbox, so "does this worker take approvals?" is one type
// assertion on the handle rather than a second look-up.
type inboxHandle struct {
	Handler
	*approvals
}

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
		b.SetHandler(w.handle)
	}
	// Then give each backend its own lifetime. A backend whose Start fails would
	// leave the worker deaf, so it is a start error.
	for _, b := range w.backends {
		if r, ok := b.(Runnable); ok {
			if err := r.Start(ctx); err != nil {
				return fmt.Errorf("iface: %s: backend start: %w", w.ID(), err)
			}
		}
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
	// Order matters. A served tool is answered by the extension registry; any other
	// event belongs to the backends, and a message a backend consumes is NOT a
	// "no handler" error — that reply is only for events nobody wants. Asking the
	// subscriptions first is what keeps a forwarded message from being answered
	// with request.failed just because it carried a request id.
	if !w.serves(evt.Type) && w.h.publish(evt) {
		return
	}
	w.DispatchExtension(evt)
}

// serves reports whether the extension registry handles this event type.
func (w *Worker) serves(typ event.EventType) bool {
	for _, ext := range w.Extensions() {
		if ext.Event == typ {
			return true
		}
	}
	return false
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
	// Backends stop before the subscriptions close, so a backend's last delivery
	// can still reach the bus.
	for i := len(w.backends) - 1; i >= 0; i-- {
		if r, ok := w.backends[i].(Runnable); ok {
			if err := r.Stop(); err != nil {
				logf("%s: backend stop: %v", w.ID(), err)
			}
		}
	}
	w.h.close()
	if w.Channel != nil {
		_ = w.Channel.Close()
	}
	return nil
}

// snapshot is the worker's durable state: its own (the approval inbox) plus the
// backend's, which stays opaque here.
//
//	{"approvals": {...}}              only the inbox
//	{"backend":   <backend bytes>}    only the backend
//	{"approvals": {...}, "backend": …} both
//
// A nil result means "nothing to persist". Recovery is a snapshot, not a replay:
// replaying an event log has no bound (how far back?), while a snapshot is exactly
// the state the worker last had.
type snapshot struct {
	Approvals *approvalsState `json:"approvals,omitempty"`
	Backend   json.RawMessage `json:"backend,omitempty"`
}

// Snapshot captures the inbox and asks the stateful backend for its own bytes.
func (w *Worker) Snapshot() ([]byte, error) {
	var snap snapshot
	if w.inbox != nil {
		snap.Approvals = w.inbox.snapshot()
	}
	if w.state != nil {
		raw, err := w.state.Snapshot()
		if err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			snap.Backend = json.RawMessage(raw)
		}
	}
	if snap.Approvals == nil && snap.Backend == nil {
		return nil, nil
	}
	return json.Marshal(snap)
}

// Restore replays a snapshot into the inbox and the stateful backend.
func (w *Worker) Restore(state []byte) error {
	if len(state) == 0 {
		return nil
	}
	var snap snapshot
	if err := json.Unmarshal(state, &snap); err == nil && (snap.Approvals != nil || snap.Backend != nil) {
		if w.inbox != nil {
			w.inbox.restore(snap.Approvals)
		}
		if w.state != nil && len(snap.Backend) > 0 {
			return w.state.Restore(snap.Backend)
		}
		return nil
	}
	// Backward compatibility: a snapshot written before the envelope existed is the
	// backend's own shape, so hand it over whole.
	if w.state != nil {
		return w.state.Restore(state)
	}
	return fmt.Errorf("iface: %s: snapshot is not this worker's", w.ID())
}

// logf is the package's single logging seam, so tests stay quiet.
func logf(format string, args ...any) {
	log.Printf("[iface] "+format, args...)
}
