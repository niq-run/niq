// Package program provides the Program Worker — a bus-facing worker that
// manages Program discovery, loading, and registration.
//
// Program Worker is a domain service worker, like GitHub Worker. It exposes
// search, load, edit, register, delete (and execute in the future) as tools on
// the bus, namespaced under this worker's ID (e.g. program__search,
// program__edit). Other workers may declare dotted tool names (e.g.
// foo.bar) — the reason worker restores those via a reverse mapping on
// dispatch, while this worker keeps flat names.
package program

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/pkg/baseworker"
)

// Config holds the configuration for a Program Worker.
type Config struct {
	ID      string
	Bus     corebus.WorkerSideChannel
	Backend program.Backend
}

// Worker is a bus-facing worker that manages Program lifecycle.
// It subscribes to its tool events and worker.discover, and exposes
// search/load/edit/register/delete tools on the bus.
//
// Programs are discovered from the Backend at startup and cached in the
// programs map. The map is the single source of truth for registered programs;
// the Backend is the persistent storage for their content files.
type Worker struct {
	baseworker.BaseWorker
	backend  program.Backend
	programs map[string]*program.Program

	started bool
	cancel  context.CancelFunc

	mu  sync.Mutex   // guards started / cancel
	pMu sync.RWMutex // guards programs map
}

// New creates a Program Worker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "program"
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		backend:  cfg.Backend,
		programs: make(map[string]*program.Program),
	}
	w.registerExtensions()
	return w
}

// Start subscribes to the bus, discovers programs from the backend,
// and begins watching for tool calls.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.started {
		return fmt.Errorf("program: already started")
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancel = cancelFn

	// Discover programs from the backend.
	if err := w.discover(ctx); err != nil {
		log.Printf("[program] discovery warning: %v", err)
	}

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.AnnounceReady("program", nil)

	w.started = true
	log.Printf("[program] started with %d programs", w.listPrograms())

	return nil
}

// Stop shuts down the Program Worker.
func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.started {
		return nil
	}

	w.cancel()
	w.cancel = nil
	w.started = false
	log.Println("[program] stopped")

	return nil
}

func (w *Worker) Snapshot() ([]byte, error)  { return nil, nil }
func (w *Worker) Restore(state []byte) error { return nil }

// listPrograms returns the count of registered programs for logging.
func (w *Worker) listPrograms() int {
	w.pMu.RLock()
	defer w.pMu.RUnlock()
	return len(w.programs)
}

// ── Program map operations ──

// register adds or replaces a program in the cache.
func (w *Worker) register(p *program.Program) error {
	if p.Name == "" {
		return fmt.Errorf("program: name is required")
	}
	w.pMu.Lock()
	defer w.pMu.Unlock()
	w.programs[p.Name] = p
	return nil
}

// get retrieves a program by name.
func (w *Worker) get(name string) (*program.Program, error) {
	w.pMu.RLock()
	defer w.pMu.RUnlock()
	p, ok := w.programs[name]
	if !ok {
		return nil, fmt.Errorf("program: %q not found", name)
	}
	return p, nil
}

// search finds programs whose name, description, or tags contain the query
// substring (case-insensitive). If ct is non-empty, results are filtered by
// ContentType.
func (w *Worker) search(query string, ct program.ContentType) ([]*program.Program, error) {
	w.pMu.RLock()
	defer w.pMu.RUnlock()

	q := strings.ToLower(query)
	var result []*program.Program
	for _, p := range w.programs {
		if ct != "" && p.ContentType != ct {
			continue
		}
		if q == "" || matchProgram(p, q) {
			result = append(result, p)
		}
	}
	return result, nil
}

// matchProgram checks whether a program's name, description, or any of its
// tags contain the given query substring (case-insensitive).
func matchProgram(p *program.Program, query string) bool {
	if strings.Contains(strings.ToLower(p.Name), query) ||
		strings.Contains(strings.ToLower(p.Description), query) {
		return true
	}
	for _, t := range p.Tags {
		if strings.Contains(strings.ToLower(t), query) {
			return true
		}
	}
	return false
}

// ── Event loop ──

func (w *Worker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(ctx, evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) process(ctx context.Context, evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerDiscover:
		if evt.WorkerId != w.ID() {
			w.AnnounceReady("program", nil)
		}
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[program %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}
