// Package directory implements the DirectoryWorker — a single, read-only
// worker that serves the fleet roster (list_workers / get_worker_info).
//
// It has NO lifecycle authority: it only LEARNS from the bus. Every worker's
// worker.ready announcement (including third-party workers connecting directly,
// not just ones the host spawned) is collected into a directory, and
// worker.gone removes a departed one. Rationale for a dedicated worker rather
// than hanging the roster off the host: giving host a list tool would let a
// reason worker misread host as being able to suspend arbitrary workers (it
// only manages self-hosted ones) or as knowing only what it spawned. Directory
// is a neutral, permission-free observer with no lifecycle surface.
package directory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/impl/baseworker"
)

// Config holds the construction inputs for a directory worker.
type Config struct {
	ID  string
	Bus corebus.WorkerSideChannel
}

// workerRecord is one worker's contract learned from its worker.ready: the
// worker type and its peer-facing capabilities (watch) and published events.
type workerRecord struct {
	Type      string           `json:"type"`
	Watch     []map[string]any `json:"tools,omitempty"`
	Publishes []map[string]any `json:"publishes,omitempty"`
}

// DirectoryWorker serves list_workers / get_worker_info from a directory it
// builds by collecting worker.ready / worker.gone on the bus.
type DirectoryWorker struct {
	baseworker.BaseWorker
	started bool
	cancel  context.CancelFunc
	mu      sync.Mutex
	dirMu   sync.Mutex
	workers map[string]workerRecord
}

// Directory tools — each its own event type (see the request-response
// convention on the bus).
const (
	TypeListWorkers   event.EventType = "list_workers"
	TypeGetWorkerInfo event.EventType = "get_worker_info"
)

// New creates a directory worker.
func New(cfg Config) *DirectoryWorker {
	id := cfg.ID
	if id == "" {
		id = "directory"
	}
	w := &DirectoryWorker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		workers:    make(map[string]workerRecord),
	}
	w.registerExtensions()
	return w
}

// Start begins the event loop, announces the directory worker (so reason
// workers learn its tools), and broadcasts a discover so every already-online
// worker answers DIRECTLY — otherwise workers that announced before this one
// subscribed would be invisible. Workers that start later announce at their
// own start (we subscribe to worker.ready).
func (w *DirectoryWorker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("directory %s: already started", w.ID())
	}

	runCtx, cancelFn := context.WithCancel(ctx)
	w.cancel = cancelFn

	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.AnnounceReady("directory", nil)

	disc := event.New(event.TypeWorkerDiscover, w.ID(), map[string]any{
		"worker_id": w.ID(),
	})
	disc.Transient = true // presence: live-only, not durable history
	_ = w.Channel.Broadcast(context.Background(), disc)

	w.started = true
	log.Printf("[directory %s] started", w.ID())
	return nil
}

// Stop terminates the worker.
func (w *DirectoryWorker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	w.cancel()
	w.cancel = nil
	w.started = false
	return nil
}

// Snapshot/Restore: the directory is rebuilt from the bus on start, so nothing
// durable needs persisting.
func (w *DirectoryWorker) Snapshot() ([]byte, error) { return nil, nil }
func (w *DirectoryWorker) Restore([]byte) error      { return nil }

func (w *DirectoryWorker) watch(ctx context.Context, busCh <-chan event.Event) {
	for {
		select {
		case evt := <-busCh:
			w.process(evt)
		case <-ctx.Done():
			return
		}
	}
}

func (w *DirectoryWorker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerReady:
		w.rememberReady(evt)
	case event.TypeWorkerGone:
		if evt.WorkerId != "" {
			w.dirMu.Lock()
			delete(w.workers, evt.WorkerId)
			w.dirMu.Unlock()
		}
	case event.TypeWorkerDiscover:
		if evt.WorkerId != w.ID() {
			w.AnnounceReadyTo(evt.WorkerId, "directory", nil, false)
		}
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[directory %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}

// rememberReady records (or replaces, since a ready is the whole contract) a
// worker in the directory. Only peer-facing watch (SelfOnly excluded) is kept.
// This worker itself is ignored (no self-directed half here).
func (w *DirectoryWorker) rememberReady(evt event.Event) {
	id := evt.WorkerId
	if id == "" || id == w.ID() {
		return
	}
	rec := workerRecord{}
	if typ, _ := evt.Payload["type"].(string); typ != "" {
		rec.Type = typ
	}
	rec.Watch = normalizeObjectList(evt.Payload["watch"])
	rec.Publishes = normalizeObjectList(evt.Payload["publishes"])
	w.dirMu.Lock()
	w.workers[id] = rec
	w.dirMu.Unlock()
	log.Printf("[directory %s] learned %s (%s, %d tools)", w.ID(), id, rec.Type, len(rec.Watch))
}

// normalizeObjectList unpacks the wire form of a watch/publishes list. In-process
// announcements carry []map[string]any; events that crossed a transport (e.g. a
// remote worker over HTTP) decode to []any of map[string]any. Both are normalized
// here so the directory roster does not silently drop a remote worker's tools.
func normalizeObjectList(v any) []map[string]any {
	switch l := v.(type) {
	case nil:
		return nil
	case []map[string]any:
		return l
	case []any:
		out := make([]map[string]any, 0, len(l))
		for _, item := range l {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

// registerExtensions binds the directory tools.
func (w *DirectoryWorker) registerExtensions() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event:       TypeListWorkers,
		Description: "List every worker on the bus as a slim roster: worker_id, type, and the short event-type list it publishes. It deliberately omits tool schemas (those are large) — call get_worker_info for one worker's full tool detail. This is the fleet directory: the one place to see who exists, including peer reason workers and third-party workers that connected directly. Peer reason workers are NOT callable tools (their ready lists events they respond to, not capabilities to invoke) but are reachable as message targets.",
		Parameters:  obj(map[string]any{}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleListWorkers(evt, tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeGetWorkerInfo,
		Description: "Full details for one worker: its type and the tools it exposes with complete parameter schemas plus published events. Use the worker_id returned by list_workers.",
		Parameters: obj(map[string]any{
			"worker": map[string]any{"type": "string",
				"description": "Worker ID, as returned by list_workers."},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleGetWorkerInfo(evt, tc)
	})
}

// handleListWorkers serves the roster: every worker learned from its ready, as
// a slim identity list — worker_id, type and the short event-type list it
// publishes. NO tool details: those schemas are large and belong to
// get_worker_info's drill-in view, so a roster stays cheap to read.
func (w *DirectoryWorker) handleListWorkers(evt event.Event, tc baseworker.ToolCall) {
	w.dirMu.Lock()
	roster := make([]map[string]any, 0, len(w.workers))
	for id, rec := range w.workers {
		roster = append(roster, map[string]any{
			"worker_id": id,
			"type":      rec.Type,
			"publishes": rec.Publishes,
		})
	}
	w.dirMu.Unlock()

	b, err := json.Marshal(roster)
	if err != nil {
		w.ReplyFailed(evt.WorkerId, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	w.ReplyCompleted(evt.WorkerId, tc.CallID, string(b), tc.TraceID)
	log.Printf("[directory %s] list_workers → %d worker(s)", w.ID(), len(roster))
}

// handleGetWorkerInfo serves one worker's full record.
func (w *DirectoryWorker) handleGetWorkerInfo(evt event.Event, tc baseworker.ToolCall) {
	target := baseworker.ArgString(tc.Args, "worker")
	if target == "" {
		w.ReplyFailed(evt.WorkerId, tc.CallID, "get_worker_info requires the 'worker' parameter (worker ID)", tc.TraceID)
		return
	}
	w.dirMu.Lock()
	rec, ok := w.workers[target]
	w.dirMu.Unlock()
	if !ok {
		w.ReplyFailed(evt.WorkerId, tc.CallID,
			fmt.Sprintf("unknown worker %q — call list_workers to see the known ids", target), tc.TraceID)
		return
	}
	b, err := json.Marshal(map[string]any{
		"worker_id": target,
		"type":      rec.Type,
		"tools":     rec.Watch,
		"publishes": rec.Publishes,
	})
	if err != nil {
		w.ReplyFailed(evt.WorkerId, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	w.ReplyCompleted(evt.WorkerId, tc.CallID, string(b), tc.TraceID)
	log.Printf("[directory %s] get_worker_info → %s", w.ID(), target)
}
