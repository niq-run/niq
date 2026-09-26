// Package history implements a read-only introspection worker that queries the
// persisted event bus store: list a worker's historical events (with per-event
// payload truncation so a listing stays compact and bounded), and fetch the
// full detail of a specific event (by id) or an entire request→response pair
// (by request_id) without any size limit — the "drill into one event" view.
//
// The worker is deliberately a participant over the bus like any other: reason
// workers (or a human HIW) discover its tools via worker.ready and call them
// as directed tool invocations. Its read side is the store.EventStore its
// builder injects — the domain backend, a pure read service that never touches
// the bus.
package history

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/store"
	"github.com/niq-run/niq/core/impl/baseworker"
)

// Default tunables for the list tool. MaxListEvents caps how many events a
// single history.list returns; MaxListItemBytes caps a single event's serialized
// payload in list mode, so a big tool result (request.completed carrying a bash
// output) degrades to a compact, flagged stub instead of blowing up the listing.
// history.get is deliberately exempt — it returns the full event, untouched.
const (
	DefaultMaxListEvents    = 50
	DefaultMaxListItemBytes = 2000
)

// Config holds the construction inputs for a history worker.
type Config struct {
	ID  string
	Bus corebus.WorkerSideChannel
	// Store is the read-only event store to query. In-process workers get the
	// project's store injected by the assembly layer (see build.go); the
	// worker never writes to it.
	Store store.EventStore
	// MaxListEvents / MaxListItemBytes tune the list tool. 0 uses the package
	// defaults. history.get ignores both.
	MaxListEvents    int
	MaxListItemBytes int
}

// Worker is a history worker: it embeds the shared base, holds a read-only
// event store as its domain backend, and serves the history.list /
// history.get tools over the bus.
type Worker struct {
	baseworker.BaseWorker
	store            store.EventStore
	maxListEvents    int
	maxListItemBytes int
	started          bool
	cancelRun        context.CancelFunc
}

// History tool event types — each is a directed tool invocation whose event
// type IS the tool name (the request-response convention in the bus). Exposed
// to peers (not SelfOnly): their whole point is cross-worker introspection.
const (
	TypeHistoryList event.EventType = "history.list"
	TypeHistoryGet  event.EventType = "history.get"
)

// New creates a history worker from the given Config.
func New(cfg Config) *Worker {
	me, mib := cfg.MaxListEvents, cfg.MaxListItemBytes
	if me <= 0 {
		me = DefaultMaxListEvents
	}
	if mib <= 0 {
		mib = DefaultMaxListItemBytes
	}
	return &Worker{
		BaseWorker:       baseworker.NewBaseWorker(cfg.ID, cfg.Bus),
		store:            cfg.Store,
		maxListEvents:    me,
		maxListItemBytes: mib,
	}
}

// ── Lifecycle ──

// Start registers the history tools as extensions, enters the event loop, and
// announces presence. Following the workspace worker's shape: the extension
// registry is the dispatch table, and AnnounceReady tells peers (reason
// workers) what the history worker serves.
func (w *Worker) Start(ctx context.Context) error {
	if w.started {
		return fmt.Errorf("history %s: already started", w.ID())
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancelRun = cancel

	w.registerTools()
	busCh, _ := w.Channel.Receive(runCtx)
	go w.watch(runCtx, busCh)
	w.AnnounceReady("history", nil)
	w.started = true
	return nil
}

// Stop terminates the worker and releases its event-loop goroutine.
func (w *Worker) Stop() error {
	if !w.started {
		return nil
	}
	w.cancelRun()
	w.cancelRun = nil
	w.started = false
	return nil
}

// Snapshot reports no durable state: a history worker is stateless (its whole
// domain is the shared event store), so nothing needs to be persisted.
func (w *Worker) Snapshot() ([]byte, error) { return nil, nil }

// Restore is a no-op for a stateless worker.
func (w *Worker) Restore(_ []byte) error { return nil }

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
		// A discoverer asked who the fleet is — answer it directly, not by a
		// broadcast re-announce (a discover storm would otherwise fan O(N²)
		// ready broadcasts across every online worker).
		if evt.WorkerId != w.ID() {
			w.AnnounceReadyTo(evt.WorkerId, "history", nil, false)
		}
	default:
		if !w.DispatchExtension(evt) {
			log.Printf("[history %s] no extension for event %s", w.ID(), evt.Type)
		}
	}
}

// registerTools binds the history.list / history.get extension handlers.
func (w *Worker) registerTools() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event:       TypeHistoryList,
		Description: "Recall PAST events from the event bus store (your own or another worker's) across time and restarts. USE this only to recover/verify historical facts NOT already in your current context — e.g. after a restart you lost your working memory, or you need a tool result you sent/ran turns ago. Each call the store returns any number of events, so always narrow with worker + types + limit (default 50) and keep the page small; the bus also appends continuously, so don't page through a whole history you only cursor-checked once. DON'T use it for present state (use list_workers/get_worker_info), for anything your current context already answers, or to satisfy idle curiosity — it costs tokens and a large listing wastes them. Events come back with payloads truncated to a per-item cap; for one event's full content, drill in with history.get.",
		Parameters: obj(map[string]any{
			"worker": map[string]any{"type": "string",
				"description": "Worker ID to filter by source, target, or recipient. Use \"*\" (default) for all workers."},
			"role": map[string]any{"type": "string", "enum": []string{"sent", "received"},
				"description": "Which side of the worker's traffic matches: sent (it is the source) or received (target/recipient). Omit for both."},
			"since": map[string]any{"type": "integer",
				"description": "Unix timestamp in seconds; only events at or after this time."},
			"type": map[string]any{"type": "string",
				"description": "Exact event type filter (e.g. request.completed, bash, reason.thinking). Shorthand for a single-entry 'types'."},
			"types": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "Restrict to one or more exact event types (e.g. [\"bash\", \"request.completed\"]). Filtering happens in the store, so it stays efficient."},
			"trace_id": map[string]any{"type": "string",
				"description": "Only events in this reasoning trace."},
			"request_id": map[string]any{"type": "string",
				"description": "Only events carrying this request id (a tool call + its reply echo it)."},
			"before": map[string]any{"type": "string",
				"description": "Event ID anchor for pagination: only events strictly before it (newest first)."},
			"limit": map[string]any{"type": "integer",
				"description": "Max events to return (default 50)."},
			"desc": map[string]any{"type": "boolean",
				"description": "True = newest first (default), false = oldest first."},
			"max_bytes": map[string]any{"type": "integer",
				"description": "Per-event payload truncation cap in bytes for this call. Omitted/0 uses the worker default (config max_list_item_bytes). Set it higher to keep more content in the listing, lower to shrink it."},
			"truncate": map[string]any{"type": "boolean",
				"description": "Whether to truncate large payloads. True (default) caps each event's payload at max_bytes. False returns every payload in full — use only when you need the content and accept a large listing (history.get is still the drill-in tool)."},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleList(tc)
	})

	w.Register(baseworker.Extension{
		Event:       TypeHistoryGet,
		Description: "Drill into the FULL, untruncated content of specific historical events. USE this only after history.list has shown you a truncated stub or given you an exact id/request_id you need — pass 'id' for one event, or 'request_id' for its whole request→response pair (the tool call and whichever request.* reply echoed it). DON'T fetch many events by hand here (that's history.list's narrowing job), and don't call it without a specific id/request_id in hand. A single result can be large (e.g. a big tool output) and is returned verbatim, so only fetch what you genuinely need — not deducible from what you already have.",
		Parameters: obj(map[string]any{
			"id": map[string]any{"type": "string",
				"description": "Event ID to fetch in full (as shown by history.list)."},
			"request_id": map[string]any{"type": "string",
				"description": "Fetch every event carrying this request id (request + its replies)."},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleGet(tc)
	})
}

// ── history.list ──

// listItem is one compact, possibly truncated event in a history.list result.
// The payload is always a string (the compact JSON encoding of the event's
// payload map). When it exceeded the per-entry byte cap it is cut with a
// marker and payload_truncated is set, signalling the caller to history.get
// the event for its real content.
type listItem struct {
	ID               string `json:"id"`
	Type             string `json:"type"`
	Worker           string `json:"worker"`
	Target           string `json:"target,omitempty"`
	TS               int64  `json:"ts"`
	TraceID          string `json:"trace_id,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	Payload          string `json:"payload"`
	PayloadTruncated bool   `json:"payload_truncated,omitempty"`
}

func (w *Worker) handleList(tc baseworker.ToolCall) {
	opts, err := w.listOpts(tc.Args)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	workerID := "*"
	if wid, _ := tc.Args["worker"].(string); wid != "" && wid != "*" {
		workerID = wid
		opts.WorkerIDs = []string{wid}
		opts.WorkerRoles = roleToStore(tc.Args["role"])
	}

	events, err := w.store.List(context.Background(), workerID, opts)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.list: %v", err), tc.TraceID)
		return
	}

	// Per-call truncation control: max_bytes overrides the worker default;
	// truncate=false disables the cap entirely (full payloads in the listing).
	capBytes := w.maxListItemBytes
	truncate := true
	if mb := baseworker.ArgInt(tc.Args, "max_bytes", -1); mb >= 0 {
		capBytes = mb
	}
	if v, ok := tc.Args["truncate"].(bool); ok {
		truncate = v
	}

	type result struct {
		Count     int        `json:"count"`
		Truncated bool       `json:"truncated,omitempty"`
		Events    []listItem `json:"events"`
	}
	res := result{Events: make([]listItem, 0, len(events))}
	var anyTruncated bool
	for _, e := range events {
		item, truncated := w.trim(e, capBytes, truncate)
		if truncated {
			anyTruncated = true
		}
		res.Events = append(res.Events, item)
	}
	res.Count = len(res.Events)
	res.Truncated = anyTruncated

	b, err := json.Marshal(res)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.list: serialize: %v", err), tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, string(b), tc.TraceID)
	log.Printf("[history %s] history.list → %d events (caller=%s)", w.ID(), res.Count, tc.CallerID)
}

// listOpts builds the store query opts for a history.list invocation, mapping
// the tool args onto store.QueryOpts. `desc` defaults open (true): a listing is
// a display view, newest first. result is that reason workers paging through a
// worker's history get the most recent activity.
func (w *Worker) listOpts(args map[string]any) (store.QueryOpts, error) {
	opts := store.QueryOpts{
		Desc:    true,
		Limit:   baseworker.ArgInt(args, "limit", w.maxListEvents),
		Since:   int64(baseworker.ArgInt(args, "since", 0)),
		TraceID: baseworker.ArgString(args, "trace_id"),
	}
	if v, ok := args["desc"].(bool); ok {
		opts.Desc = v
	}
	if rid := baseworker.ArgString(args, "request_id"); rid != "" {
		opts.RequestID = rid
	}
	// Event-type filter: 'types' (array) wins over the single 'type' shorthand;
	// both are passed to the store so filtering happens at query time.
	if types := baseworker.ArgStrings(args, "types"); len(types) > 0 {
		opts.Types = types
	} else if t := baseworker.ArgString(args, "type"); t != "" {
		opts.Types = []string{t}
	}
	if opts.Limit <= 0 || opts.Limit > w.maxListEvents {
		opts.Limit = w.maxListEvents
	}
	return opts, nil
}

// trim renders one event as a compact listItem. When truncation is enabled,
// the payload string is capped at capBytes (0 means *only* truncate when the
// worker's configured limit decides — see callers; here capBytes is always
// >=1 for the truncate=true path) and marked truncated. With truncate=false,
// the full payload is returned verbatim and never flagged. Returns whether
// truncation happened.
func (w *Worker) trim(e event.Event, capBytes int, truncate bool) (listItem, bool) {
	raw, err := json.Marshal(e.Payload)
	if err != nil || raw == nil {
		raw = []byte("null")
	}
	truncated := truncate && len(raw) > capBytes
	if truncated {
		raw = append(raw[:capBytes], []byte(" …[truncated; use history.get]")...)
	}
	return listItem{
		ID:               e.ID,
		Type:             string(e.Type),
		Worker:           e.WorkerId,
		Target:           e.TargetWorkerID,
		TS:               e.Timestamp,
		TraceID:          e.TraceID,
		RequestID:        e.RequestId,
		Payload:          string(raw),
		PayloadTruncated: truncated,
	}, truncated
}

// ── history.get ──

func (w *Worker) handleGet(tc baseworker.ToolCall) {
	id := baseworker.ArgString(tc.Args, "id")
	req := baseworker.ArgString(tc.Args, "request_id")

	ctx := context.Background()
	var events []event.Event

	switch {
	case id != "":
		evt, found, err := w.store.Get(ctx, id)
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.get: %v", err), tc.TraceID)
			return
		}
		if !found {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.get: no event with id %q", id), tc.TraceID)
			return
		}
		events = []event.Event{evt}
	case req != "":
		var err error
		events, err = w.store.List(ctx, "*", store.QueryOpts{RequestID: req})
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.get: %v", err), tc.TraceID)
			return
		}
		if len(events) == 0 {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.get: no events carry request_id %q", req), tc.TraceID)
			return
		}
	default:
		w.ReplyFailed(tc.CallerID, tc.CallID, "history.get requires 'id' (one event) or 'request_id' (a request→response pair)", tc.TraceID)
		return
	}

	// Full, untruncated detail: no trim here, the structured payload is what
	// the caller came for.
	b, err := json.Marshal(events)
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("history.get: serialize: %v", err), tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, string(b), tc.TraceID)
	log.Printf("[history %s] history.get → %d event(s) (caller=%s)", w.ID(), len(events), tc.CallerID)
}

// roleToStore maps a history.list "role" arg to the store role filter. "sent" /
// "received" translate directly; anything else (empty etc.) means both.
func roleToStore(v any) []string {
	switch r, _ := v.(string); r {
	case store.RoleSent:
		return []string{store.RoleSent}
	case store.RoleReceived:
		return []string{store.RoleReceived}
	}
	return nil
}
