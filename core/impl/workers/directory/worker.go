// Package directory implements the DirectoryWorker — a single, read-only
// worker that serves the fleet roster with two tools: search (the roster, or a
// keyword filter over it) and describe (one worker's contract).
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
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/niq-run/niq/core/impl/baseworker"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// Config holds the construction inputs for a directory worker.
type Config struct {
	ID  string
	Bus corebus.WorkerSideChannel
	// Params is the declaration's raw construction params, read only for the
	// readme customization (params.readme / readme_append and {{key}} placeholders).
	Params map[string]any
}

// workerRecord is one worker's contract learned from its worker.ready: the
// worker type and its peer-facing capabilities (watch) and published events.
type workerRecord struct {
	Type      string           `json:"type"`
	Watch     []map[string]any `json:"tools,omitempty"`
	Publishes []map[string]any `json:"publishes,omitempty"`
}

// DirectoryWorker serves search / describe from a directory it builds by
// collecting worker.ready / worker.gone on the bus.
type DirectoryWorker struct {
	baseworker.BaseWorker
	params  map[string]any // declaration params, for the readme customization
	started bool
	cancel  context.CancelFunc
	mu      sync.Mutex
	dirMu   sync.Mutex
	workers map[string]workerRecord
}

// Directory tools — each its own event type (see the request-response
// convention on the bus).
const (
	// TypeSearch answers both "who is here" and "which one do I mean": with no
	// query it is the roster, with a query it is a keyword search. One event
	// rather than a list/search pair because the two differ only in whether a
	// filter was supplied, not in what they can return.
	TypeSearch event.EventType = "search"
	// TypeDescribe returns one worker's contract: its type, the events it
	// serves with their schemas, and the events it publishes.
	TypeDescribe event.EventType = "describe"
)

// New creates a directory worker.
func New(cfg Config) *DirectoryWorker {
	id := cfg.ID
	if id == "" {
		id = "directory"
	}
	w := &DirectoryWorker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		params:     cfg.Params,
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
	w.rememberSelf()

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

func (w *DirectoryWorker) process(evt event.Event) {
	switch evt.Type {
	case event.TypeWorkerReady:
		w.rememberReady(evt)
	case event.TypeWorkerGone:
		// A gone notice about this worker would be a mistake: the directory is
		// always in its own roster.
		if evt.WorkerId != "" && evt.WorkerId != w.ID() {
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

// rememberSelf records this worker in its own roster. Peers learn the directory
// from its ready broadcast, which excludes the sender — so without this entry a
// caller asking "who is here", or "describe the directory worker", would not find
// the one worker it is already talking to. The record is built from the extension
// registry rather than off the wire, so it carries exactly what the announcement
// carries (the peer-facing contract, Internal left out).
func (w *DirectoryWorker) rememberSelf() {
	w.dirMu.Lock()
	w.workers[w.ID()] = workerRecord{
		Type:  "directory",
		Watch: w.WatchEntries(false),
	}
	w.dirMu.Unlock()
}

// rememberReady records (or replaces, since a ready is the whole contract) a
// worker in the directory. Only peer-facing watch (Internal excluded) is kept.
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

	w.RegisterReadme(
		"Read this worker's usage guide: the fleet roster, its two tools, and how to use them.",
		w.readmeText, w.params)

	w.Register(baseworker.Extension{
		Event: TypeSearch,
		Response: obj(map[string]any{
			"query": map[string]any{"type": "string"},
			"total": map[string]any{"type": "number", "description": "matches before the limit is applied"},
			"workers": map[string]any{"type": "array",
				"items": obj(map[string]any{
					"worker_id": map[string]any{"type": "string"},
					"type":      map[string]any{"type": "string"},
					"matched":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "where the query matched (absent for the unfiltered roster)"},
				})},
		}),
		Description: "Look up workers on the bus. With no query it is the roster: every worker as worker_id + type — the cheap answer to who exists, including peer reason workers and third-party workers that connected directly. With a query it is a keyword search over ids, types, and the tools/events each worker exposes (case-insensitive; every whitespace-separated term must match somewhere). Hits are slim — id, type, where it matched — so confirm with describe or worker.readme. Peer reason workers are listed but are NOT callable tools (their ready lists events they respond to, not capabilities to invoke); reach them with message.",
		Parameters: obj(map[string]any{
			"query": map[string]any{"type": "string",
				"description": "Optional keywords, e.g. \"fish\", \"web search\". Omit for the full roster."},
			"limit": map[string]any{"type": "integer",
				"description": "Max hits when a query is given (default 20, max 100); ignored for the roster. The response's total counts all matches."},
			"types": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "Only workers of these types (e.g. [\"workspace\", \"program\"]). Case-insensitive; combines with exclude_types."},
			"exclude_types": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "Drop workers of these types (e.g. [\"reason\", \"coding\"]). Useful when looking for a capability and the other thinkers are noise."},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleSearch(evt, tc)
	})

	w.Register(baseworker.Extension{
		Event: TypeDescribe,
		Response: obj(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"worker_id": map[string]any{"type": "string"},
				"type":      map[string]any{"type": "string"},
				"tools": map[string]any{"type": "array", "description": "each entry: event, desc, parameters (request schema), response (reply schema, when declared)",
					"items": obj(map[string]any{
						"event":      map[string]any{"type": "string"},
						"desc":       map[string]any{"type": "string"},
						"parameters": map[string]any{"type": "object"},
						"response":   map[string]any{"type": "object"},
					})},
				"publishes": map[string]any{"type": "array"},
			},
		}),
		Description: "Full details for one worker: its type, the tools it exposes with complete parameter schemas plus published events. Use a worker_id from search.",
		Parameters: obj(map[string]any{
			"worker": map[string]any{"type": "string",
				"description": "Worker ID, as returned by search."},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.handleDescribe(evt, tc)
	})

	w.registerHelp()
}

// directoryReadme is the agent-facing usage guide returned by worker.readme.
const directoryReadme = `# directory — fleet roster

This worker is a read-only observer of the bus. It learns every worker from its
worker.ready and forgets it on worker.gone; it has no lifecycle authority.

## Tools

- search { query?, limit? } — no query: the roster (worker_id + type only, no
  schemas, no publishes). With a query: keyword search over worker ids, types
  and the tools/events they expose — case-insensitive, every whitespace-
  separated term must match somewhere. Hits are slim (id, type, where it
  matched).
- describe { "worker": "<id>" } — one worker's full contract: type, its tools
  with complete parameter schemas, and its published events.

## How to use

1. search { query } — recall a worker by what it does when you do not know its
   id; search {} returns the whole roster.
2. describe { worker } — inspect a candidate before calling or messaging it.
3. worker.readme on a candidate reason worker — read its own description (what
   it is responsible for and what it has recently been working on) to confirm it
   is the one you remember, and whether it is already busy with this work.

## Notes

- Search covers the announced contract only (id, type, tool events and their
  descriptions, published events). A worker's readme is NOT searched — read it
  with worker.readme once you have a shortlist.
- Peer reason workers appear in the roster but are NOT callable tools: their
  ready lists the events they respond to, not capabilities to invoke. They are
  reachable as message targets.
- The roster is rebuilt from the bus at start; it is never persisted. This
  worker is in its own roster too, so "who is here" includes the directory.`

// readmeText is the guide plus the number of workers currently in the roster.
func (w *DirectoryWorker) readmeText() string {
	w.dirMu.Lock()
	n := len(w.workers)
	w.dirMu.Unlock()
	return fmt.Sprintf("%s\n\n## Current\n\n- workers known: %d\n", directoryReadme, n)
}

// handleSearch answers both "who is here" and "which one do I mean". With no
// query it returns the whole roster (worker_id + type); with a query it narrows
// by the announced contract. One event instead of a list/search pair: the two
// differ only in whether a filter was supplied, and neither carries tool schemas
// — that is describe's drill-in job, so a roster over hundreds of workers stays
// cheap to read.
func (w *DirectoryWorker) handleSearch(evt event.Event, tc baseworker.ToolCall) {
	query := strings.TrimSpace(baseworker.ArgString(tc.Args, "query"))
	allowType := typeFilter(tc.Args)

	if query == "" {
		w.dirMu.Lock()
		roster := make([]searchHit, 0, len(w.workers))
		for id, rec := range w.workers {
			if !allowType(rec.Type) {
				continue
			}
			roster = append(roster, searchHit{WorkerID: id, Type: rec.Type})
		}
		w.dirMu.Unlock()
		sort.Slice(roster, func(i, j int) bool { return roster[i].WorkerID < roster[j].WorkerID })

		w.ReplyCompleted(evt.WorkerId, tc.CallID, map[string]any{
			"query":   "",
			"total":   len(roster),
			"workers": roster,
		}, tc.TraceID)
		log.Printf("[directory %s] search (roster) → %d worker(s)", w.ID(), len(roster))
		return
	}

	lowerQuery := strings.ToLower(query)
	terms := strings.Fields(lowerQuery)
	limit := baseworker.ArgInt(tc.Args, "limit", 20)
	if limit <= 0 {
		limit = 20
	} else if limit > 100 {
		limit = 100
	}

	w.dirMu.Lock()
	hits := make([]searchHit, 0)
	for id, rec := range w.workers {
		if !allowType(rec.Type) {
			continue
		}
		if hit, ok := matchWorker(id, rec, terms, lowerQuery); ok {
			hits = append(hits, hit)
		}
	}
	w.dirMu.Unlock()

	total := len(hits)
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].WorkerID < hits[j].WorkerID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	w.ReplyCompleted(evt.WorkerId, tc.CallID, map[string]any{
		"query":   query,
		"total":   total,
		"workers": hits,
	}, tc.TraceID)
	log.Printf("[directory %s] search %q → %d/%d hit(s)", w.ID(), query, len(hits), total)
}

// handleDescribe serves one worker's full record: the drill-in half of the pair.
func (w *DirectoryWorker) handleDescribe(evt event.Event, tc baseworker.ToolCall) {
	target := baseworker.ArgString(tc.Args, "worker")
	if target == "" {
		w.ReplyFailed(evt.WorkerId, tc.CallID, "describe requires the 'worker' parameter (worker ID)", tc.TraceID)
		return
	}
	w.dirMu.Lock()
	rec, ok := w.workers[target]
	w.dirMu.Unlock()
	if !ok {
		w.ReplyFailed(evt.WorkerId, tc.CallID,
			fmt.Sprintf(`unknown worker %q — send "search" to %q to see the known ids`, target, w.ID()), tc.TraceID)
		return
	}
	w.ReplyCompleted(evt.WorkerId, tc.CallID, map[string]any{
		"worker_id": target,
		"type":      rec.Type,
		"tools":     rec.Watch,
		"publishes": rec.Publishes,
	}, tc.TraceID)
	log.Printf("[directory %s] describe → %s", w.ID(), target)
}

// typeFilter builds the predicate behind search's "types" / "exclude_types"
// arguments: an include list (empty = any) minus an exclude list. Matching is
// case-insensitive, and an excluded type always wins over an included one so the
// two arguments compose predictably.
func typeFilter(args map[string]any) func(string) bool {
	include := make(map[string]bool)
	for _, t := range baseworker.ArgStrings(args, "types") {
		include[strings.ToLower(strings.TrimSpace(t))] = true
	}
	exclude := make(map[string]bool)
	for _, t := range baseworker.ArgStrings(args, "exclude_types") {
		exclude[strings.ToLower(strings.TrimSpace(t))] = true
	}
	return func(workerType string) bool {
		t := strings.ToLower(workerType)
		if exclude[t] {
			return false
		}
		if len(include) > 0 && !include[t] {
			return false
		}
		return true
	}
}

// searchHit is one slim search result: the identity plus the contract fields the
// query matched, so the caller can tell why it surfaced without a second call.
// score is ranking-only and never serialized.
type searchHit struct {
	WorkerID string   `json:"worker_id"`
	Type     string   `json:"type"`
	Matched  []string `json:"matched,omitempty"`
	score    int
}

// matchWorker reports whether every term occurs somewhere in the worker's
// contract and, if so, the slim hit. matched lists at most 5 labels (id, type,
// tool:<event>, publishes:<event>) so a broad keyword cannot bloat the result.
func matchWorker(id string, rec workerRecord, terms []string, lowerQuery string) (searchHit, bool) {
	type field struct{ label, text string }
	lid := strings.ToLower(id)
	fields := make([]field, 0, 2+len(rec.Watch)+len(rec.Publishes))
	fields = append(fields, field{"id", lid})
	if ltype := strings.ToLower(rec.Type); ltype != "" {
		fields = append(fields, field{"type", ltype})
	}
	for _, t := range rec.Watch {
		evtName := strings.ToLower(stringField(t, "event"))
		if evtName == "" {
			continue
		}
		fields = append(fields, field{"tool:" + evtName, evtName + " " + strings.ToLower(stringField(t, "desc"))})
	}
	for _, p := range rec.Publishes {
		evtName := strings.ToLower(stringField(p, "type"))
		if evtName == "" {
			continue
		}
		fields = append(fields, field{"publishes:" + evtName, evtName + " " + strings.ToLower(stringField(p, "desc"))})
	}

	matched := make([]string, 0, 4)
	seen := make(map[string]bool, 8)
	for _, term := range terms {
		found := false
		for _, f := range fields {
			if !strings.Contains(f.text, term) {
				continue
			}
			found = true
			if !seen[f.label] && len(matched) < 5 {
				seen[f.label] = true
				matched = append(matched, f.label)
			}
		}
		if !found {
			return searchHit{}, false
		}
	}

	score := 0
	switch {
	case lid == lowerQuery:
		score += 100
	case strings.HasPrefix(lid, lowerQuery):
		score += 40
	case strings.Contains(lid, lowerQuery):
		score += 20
	}
	if strings.Contains(strings.ToLower(rec.Type), lowerQuery) {
		score += 10
	}
	score += 2 * len(matched)
	return searchHit{WorkerID: id, Type: rec.Type, Matched: matched, score: score}, true
}

// stringField reads a string field from a decoded watch/publishes entry.
func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}
