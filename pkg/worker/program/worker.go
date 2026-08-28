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

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/program"
	"github.com/54c1/niq/core/worker"
)

// Config holds the configuration for a Program Worker.
type Config struct {
	ID      string
	Bus     corebus.WorkerSideChannel
	Backend program.Backend
}

// Worker is a bus-facing worker that manages Program lifecycle.
// It subscribes to tool.request and worker.discover, and exposes
// search/load/edit/register/delete tools on the bus.
//
// Programs are discovered from the Backend at startup and cached in the
// programs map. The map is the single source of truth for registered programs;
// the Backend is the persistent storage for their content files.
type Worker struct {
	worker.BaseWorker
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
	return &Worker{
		BaseWorker: worker.NewBaseWorker(id, []event.EventPattern{
			event.NewPattern(event.TypeToolRequest),
			event.NewPattern(event.TypeWorkerDiscover),
		}, cfg.Bus),
		backend:  cfg.Backend,
		programs: make(map[string]*program.Program),
	}
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
	w.publishReady()

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
			w.publishReady()
		}
	case event.TypeToolRequest:
		if evt.TargetWorkerID != "" && evt.TargetWorkerID != w.ID() {
			return
		}
		w.handleToolCall(ctx, evt)
	}
}

// publishReady announces this worker's tools on the bus.
func (w *Worker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "program",
		"tools": []map[string]any{
			{
				"name":        "search",
				"description": "Search for available programs by name, description, or tag. Returns matching programs with their metadata (name, content_type, description, tags, locked). Use load to read the actual content.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "Search query — matches against program name, description, and tags (case-insensitive substring).",
						},
						"content_type": map[string]any{
							"type":        "string",
							"description": "Optional filter: 'instruction' or 'playbook'.",
							"enum":        []string{"instruction", "playbook"},
						},
					},
					"required": []any{"query"},
				},
			},
			{
				"name":        "load",
				"description": "Load a specific content file from a program. Use this to progressively load sub-contents after reading the entry content via the search tool.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"program": map[string]any{
							"type":        "string",
							"description": "Program name.",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Relative path to the content file within the program directory (e.g. 'rules/go.md').",
						},
					},
					"required": []any{"program", "path"},
				},
			},
			{
				"name":        "edit",
				"description": "Edit a program's content file with an atomic find-and-replace. Cannot edit locked programs.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"program": map[string]any{
							"type":        "string",
							"description": "Program name (also the directory name).",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Relative path to the content file within the program directory (e.g. 'PROGRAM.md' or 'rules/go.md').",
						},
						"old_text": map[string]any{
							"type":        "string",
							"description": "The exact text to find and replace.",
						},
						"new_text": map[string]any{
							"type":        "string",
							"description": "The replacement text. May be empty to delete the matched text.",
						},
					},
					"required": []any{"program", "path", "old_text"},
				},
			},
			{
				"name":        "register",
				"description": "Register a new program at runtime. Creates the program directory and PROGRAM.md file on disk. Cannot modify or overwrite locked programs.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "Program name (also used as the directory name).",
						},
						"content_type": map[string]any{
							"type":        "string",
							"description": "Program type: 'instruction' or 'playbook'.",
							"enum":        []string{"instruction", "playbook"},
						},
						"description": map[string]any{
							"type":        "string",
							"description": "Short description of what this program does.",
						},
						"tags": map[string]any{
							"type":        "array",
							"description": "Tags for search and categorization.",
							"items":       map[string]any{"type": "string"},
						},
						"content": map[string]any{
							"type":        "string",
							"description": "The entry content body (the part after the YAML frontmatter).",
						},
					},
					"required": []any{"name", "content_type", "content"},
				},
			},
			{
				"name":        "delete",
				"description": "Delete a program and all its contents. Cannot delete locked programs.",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name": map[string]any{
							"type":        "string",
							"description": "Program name to delete.",
						},
					},
					"required": []any{"name"},
				},
			},
		},
	}))
}
