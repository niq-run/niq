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
// The backend is the single source of truth: Programs are re-listed on every
// tool call, so changes made on disk after startup are picked up without a
// restart. The worker never sees a filesystem path — it passes abstract
// addresses ("{name}" for a Program's entry, "{name}/path" for a content) and
// lets the backend resolve them.
type Worker struct {
	baseworker.BaseWorker
	backend program.Backend

	started bool
	cancel  context.CancelFunc

	mu sync.Mutex // guards started / cancel
}

// New creates a Program Worker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "program"
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		backend:    cfg.Backend,
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

	// List once so a broken program root is reported at startup rather than
	// silently on the first tool call. The result is not retained — every
	// tool call re-lists.
	progs, err := w.backend.List(ctx)
	if err != nil {
		log.Printf("[program] discovery warning: %v", err)
	}

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.AnnounceReady("program", nil)

	w.started = true
	log.Printf("[program] started with %d programs", len(progs))

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

// ── Program lookup ──

// search returns the Programs whose name, description, or tags contain the
// query substring (case-insensitive). If ct is non-empty, results are filtered
// by ContentType.
//
// The backend is scanned on every call rather than cached, so Programs added
// or edited on disk after startup are found without a restart.
func (w *Worker) search(ctx context.Context, query string, ct program.ContentType) ([]*program.Program, error) {
	progs, err := w.backend.List(ctx)
	if err != nil {
		return nil, err
	}

	q := strings.ToLower(query)
	var result []*program.Program
	for _, p := range progs {
		if ct != "" && p.ContentType != ct {
			continue
		}
		if q == "" || matchProgram(p, q) {
			result = append(result, p)
		}
	}
	return result, nil
}

// programForPath returns the Program that owns an address: either the Program
// itself (root path) or the one whose path prefixes it. Only the tools that
// need a Program's metadata — the locked check in edit/delete/register — use
// it; load goes straight to the backend.
func (w *Worker) programForPath(ctx context.Context, addr string) (*program.Program, error) {
	progs, err := w.backend.List(ctx)
	if err != nil {
		return nil, err
	}
	var match *program.Program
	for _, p := range progs {
		if p.Path == addr || strings.HasPrefix(addr, p.Path+"/") {
			if match == nil || len(p.Path) > len(match.Path) {
				match = p
			}
		}
	}
	if match == nil {
		return nil, fmt.Errorf("program: %q not found", addr)
	}
	return match, nil
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
