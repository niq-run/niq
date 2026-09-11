package eventbus

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"
)

// Engine is the core event routing engine — the "ball".
//
// It holds the channels of all online workers and the identity registry.
// When a Request arrives (via HandleRequest), it routes the event to the
// appropriate target channels:
//
//   - Send: looks up the target worker's channel and delivers directly
//   - Broadcast: looks up all online workers whose SubscribeAllow matches
//     the event type, and delivers to each
//
// Engine has no goroutines. It is a data structure with methods, called
// by the Attach/watch goroutines or by control-plane code.
type Engine struct {
	mu       sync.RWMutex
	channels map[string]corebus.BusSideChannel
	registry corebus.IdentityRegistry
	store    store.AppendStore   // optional, nil to disable persistence
	onEvent  []func(event.Event) // optional hooks, called for every routed event

	// persistStats tracks persistence outcomes for live observability.
	// persistTotal counts every non-transient event handed to the store;
	// persistDropped counts those the store rejected (e.g. SQLITE_BUSY),
	// which are still delivered live to SSE subscribers but never recorded
	// in the event store — the exact gap behind "no reply in event query".
	persistTotal   atomic.Uint64
	persistDropped atomic.Uint64

	// transientReqs maps a request_id → expiry for requests that were marked
	// Transient (e.g. read-only queries the human UI initiates). A reply that
	// echoes such a request_id is also treated as non-durable, propagating the
	// request's transient nature to its response without touching workers.
	trMu          sync.Mutex
	transientReqs map[string]time.Time
}

// PersistStats returns cumulative persistence counters for observation.
func (e *Engine) PersistStats() (total, dropped uint64) {
	return e.persistTotal.Load(), e.persistDropped.Load()
}

// NewEngine creates a new Engine.
// store may be nil; if nil, events are not persisted.
func NewEngine(registry corebus.IdentityRegistry, store store.AppendStore) *Engine {
	return &Engine{
		channels:     make(map[string]corebus.BusSideChannel),
		registry:     registry,
		store:        store,
		transientReqs: make(map[string]time.Time),
	}
}

// Connect registers a worker as online by associating its channel.
// The worker must have a registered identity.
//
// A reconnect for the same worker id replaces any existing (now stale)
// connection: a network worker process (e.g. a supervisor-restarted one)
// reconnects before the bus finishes tearing down the old channel, and
// rejecting that would leave the fresh worker dead on the bus. The old
// channel is closed so its watch goroutine exits; DisconnectChannel is
// what actually removes it, and only if it is still the live connection.
func (e *Engine) Connect(workerID string, ch corebus.BusSideChannel) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.registry.Lookup(workerID); !ok {
		return fmt.Errorf("eventbus: cannot connect %s: identity not registered", workerID)
	}
	if old, ok := e.channels[workerID]; ok {
		if old == ch {
			return fmt.Errorf("eventbus: worker %s already connected", workerID)
		}
		e.channels[workerID] = ch
		if closer, _ := old.(interface{ Close() error }); closer != nil {
			_ = closer.Close()
		}
		log.Printf("[eventbus] worker %s reconnected (replaced stale connection)", workerID)
		return nil
	}
	e.channels[workerID] = ch
	log.Printf("[eventbus] worker %s connected", workerID)
	return nil
}

// Disconnect marks a worker as offline, removes its channel, and broadcasts a
// worker.gone presence event so peers (e.g. reason's discovery) drop its
// tools/events. Called on delete, suspend, crash and shutdown.
func (e *Engine) Disconnect(workerID string) {
	e.mu.Lock()
	if _, ok := e.channels[workerID]; !ok {
		e.mu.Unlock()
		return
	}
	delete(e.channels, workerID)
	e.mu.Unlock()
	log.Printf("[eventbus] worker %s disconnected", workerID)

	e.broadcastGone(context.Background(), workerID)
}

// DisconnectChannel removes a worker's connection only if it is the given
// channel instance. A watch goroutine (or SSE teardown) owns one connection;
// when that connection has already been replaced by a reconnect, removing its
// own stale handle must not knock out the newer live connection.
func (e *Engine) DisconnectChannel(workerID string, ch corebus.BusSideChannel) {
	e.mu.Lock()
	if cur, ok := e.channels[workerID]; !ok || cur != ch {
		e.mu.Unlock()
		return
	}
	delete(e.channels, workerID)
	e.mu.Unlock()
	log.Printf("[eventbus] worker %s channel closed, disconnected", workerID)

	e.broadcastGone(context.Background(), workerID)
}

// broadcastGone advertises a worker's departure as a worker.gone broadcast.
// from is the departed worker; the payload names it so reason's discovery can
// age it out. Best-effort. It bypasses the publisher ACL because it is a
// bus-authored presence event, not a publish by the worker (see below).
func (e *Engine) broadcastGone(ctx context.Context, workerID string) {
	evt := event.New(event.TypeWorkerGone, workerID, map[string]any{
		"worker_id": workerID,
	})
	evt.Transient = true // presence: delivered live, not durable history
	// The departed worker is excluded from its own broadcast (it cannot
	// receive anyway — it is offline), but set it so a straggler reconnecting
	// under the same id does not observe its own gone event.
	evt.ExcludeWorkerID = workerID

	// This is a bus-authored presence event on the departing worker's behalf,
	// not a publish by the worker — the bus must be able to announce departure
	// even when the worker's PublishAllow would not grant worker.gone.
	e.mu.RLock()
	e.broadcastLocked(ctx, evt, workerID)
	e.mu.RUnlock()
}

// HandleRequest processes a delivery request from a worker.
// from is the worker ID that sent the request.
func (e *Engine) HandleRequest(ctx context.Context, req corebus.Request, from string) {
	switch req.Type {
	case corebus.RequestSend:
		e.handleSend(ctx, req, from)
	case corebus.RequestBroadcast:
		e.handleBroadcast(ctx, req, from)
	default:
		log.Printf("[eventbus] unknown request type: %s (from %s)", req.Type, from)
	}
}

// handleSend routes events to specific target workers.
func (e *Engine) handleSend(ctx context.Context, req corebus.Request, from string) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, evt := range req.Events {
		evt.WorkerId = from // ensure identity is set by bus, not by sender
		// Only override the trace when the request actually carries one; the
		// in-process transport leaves req.TraceID empty, so dropping it would
		// clobber the trace_id the sender already stamped on the event.
		if req.TraceID != "" {
			evt.TraceID = req.TraceID
		}
		// For a single-target send, record the target so consumers (WebUI,
		// event filters) can see where the event was addressed. Multi-target
		// sends are ambiguous, so TargetWorkerID is left empty.
		if len(req.Targets) == 1 {
			evt.TargetWorkerID = req.Targets[0]
		}

		delivered := 0
		for _, target := range req.Targets {
			if !e.publishSendAllowed(from, evt.Type, target) {
				log.Printf("[eventbus] send: %s denied publish %s to %s", from, evt.Type, target)
				continue
			}
			ch, ok := e.channels[target]
			if !ok {
				log.Printf("[eventbus] send: target %s not online (from %s)", target, from)
				continue
			}
			if err := ch.Send(ctx, evt); err != nil {
				log.Printf("[eventbus] send: deliver to %s failed: %v", target, err)
				continue
			}
			delivered++
		}

		// Persist only events that were actually delivered; a fully denied
		// publish is not a real bus event and does not reach the audit log.
		if delivered > 0 {
			evt.Recipients = req.Targets
			e.persistEvent(ctx, evt)
		}
	}
}

// handleBroadcast routes events to all online workers whose SubscribeAllow
// matches the event type. The sender also receives the broadcast if its
// subscription matches.
func (e *Engine) handleBroadcast(ctx context.Context, req corebus.Request, from string) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	for _, evt := range req.Events {
		evt.WorkerId = from
		if !e.publishBroadcastAllowed(from, evt.Type) {
			log.Printf("[eventbus] broadcast: %s denied publish %s", from, evt.Type)
			continue
		}
		// Preserve the trace_id the sender stamped on the event when the request
		// carries none (the in-process transport never sets req.TraceID).
		if req.TraceID != "" {
			evt.TraceID = req.TraceID
		}

		e.broadcastLocked(ctx, evt, from)
	}
}

// broadcastLocked delivers a broadcast to every matching online subscriber. It
// must be called with e.mu held (RLock). The publisher ACL has already been
// checked by the caller.
func (e *Engine) broadcastLocked(ctx context.Context, evt event.Event, from string) {
	// Find all targets: online workers whose SubscribeAllow matches.
	var targets []string
	for workerID, ch := range e.channels {
		if evt.ExcludeWorkerID == workerID {
			continue
		}
		identity, ok := e.registry.Lookup(workerID)
		if !ok {
			continue
		}
		if !patternMatchesAny(evt, identity.SubscribeAllow) {
			continue
		}
		targets = append(targets, workerID)
		if err := ch.Send(ctx, evt); err != nil {
			log.Printf("[eventbus] broadcast: deliver to %s failed: %v", workerID, err)
		}
	}

	evt.Recipients = targets
	log.Printf("[eventbus] broadcast: %s from %s to %d worker(s)", evt.Type, from, len(targets))
	e.persistEvent(ctx, evt)
}

// OnEvent registers a callback invoked for every event routed via
// HandleRequest. Used by Stream to push events to observers.
// Multiple callbacks can be registered; they are called in order.
func (e *Engine) OnEvent(fn func(event.Event)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onEvent = append(e.onEvent, fn)
}

// persistEvent writes the event to the store if configured, and
// calls the onEvent hook for streaming. Events marked Transient by their source
// (streaming deltas, partial tool output) are delivered live to observers but
// not persisted, so they don't crowd real messages out of the replay window.
func (e *Engine) persistEvent(ctx context.Context, evt event.Event) {
	durable := shouldPersist(evt)
	if evt.RequestId != "" {
		if evt.Transient {
			// A transient (read-only, human-UI) request: record it so its
			// request_id-correlated reply is non-durable too.
			e.noteTransientReq(evt.RequestId)
		} else if durable && isReplyType(evt.Type) && e.isTransientReq(evt.RequestId) {
			// Reply echoes a transient request's id: keep it live, out of history.
			durable = false
		}
	}
	if e.store != nil && durable {
		e.persistTotal.Add(1)
		if err := e.store.Append(ctx, evt); err != nil {
			dropped := e.persistDropped.Add(1)
			// Tag reply events explicitly: a dropped request.* reply is the
			// symptom the operator is hunting ("tool ran, but no reply in
			// event query"). It still reached SSE consumers (reason worker,
			// TalkView) below, so the conversation looks fine while the
			// persisted history silently misses it.
			tag := ""
			if isReplyType(evt.Type) {
				tag = " [REPLY DROPPED — conversation advanced but store missed it]"
			}
			log.Printf("[eventbus] persist DROPPED type=%s id=%s worker=%s target=%s request_id=%s err=%v%s (persisted=%d dropped=%d)",
				evt.Type, evt.ID, evt.WorkerId, evt.TargetWorkerID, evt.RequestId, err, tag,
				e.persistTotal.Load()-dropped, dropped)
		}
	}
	for _, fn := range e.onEvent {
		fn(evt)
	}
}

// shouldPersist reports whether an event should be written to the durable
// store. Only events not marked Transient are persisted; presence/discovery
// chatter (worker.ready / discover / gone) and streaming deltas set Transient
// at the source, so they stream live over SSE but never crowd durable history
// (which is what pushed real conversations far back and broke pagination).
func shouldPersist(evt event.Event) bool {
	return !evt.Transient
}

// transientReqTTL bounds how long a transient request's id is remembered so its
// reply stays non-durable. A reply arrives within seconds of its request, so
// a short window is enough.
const transientReqTTL = 30 * time.Second

// noteTransientReq records a transient request's id (with an expiry) so a
// later reply echoing it can be treated as non-durable.
func (e *Engine) noteTransientReq(id string) {
	e.trMu.Lock()
	defer e.trMu.Unlock()
	e.transientReqs[id] = time.Now().Add(transientReqTTL)
}

// isTransientReq reports whether the given request_id belonged to a transient
// (non-durable) request. Prunes expired entries on each call.
func (e *Engine) isTransientReq(id string) bool {
	e.trMu.Lock()
	defer e.trMu.Unlock()
	now := time.Now()
	for rid, exp := range e.transientReqs {
		if now.After(exp) {
			delete(e.transientReqs, rid)
		}
	}
	_, ok := e.transientReqs[id]
	return ok
}

// isReplyType reports whether an event is a request.* reply (completed/failed/
// rejected). These are the events whose loss is most visible: a tool runs, the
// caller sees the result, but the event query/reply pairing shows nothing.
func isReplyType(t event.EventType) bool {
	return t == event.TypeRequestCompleted ||
		t == event.TypeRequestFailed ||
		t == event.TypeRequestRejected
}

// Channel returns the BusSideChannel for a connected worker, or nil.
// Used by control-plane code for direct access (e.g., health checks).
func (e *Engine) Channel(workerID string) corebus.BusSideChannel {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.channels[workerID]
}

// OnlineWorkers returns the IDs of all currently connected workers.
func (e *Engine) OnlineWorkers() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	ids := make([]string, 0, len(e.channels))
	for id := range e.channels {
		ids = append(ids, id)
	}
	return ids
}

// Lookup returns the identity for a worker ID, or false if not found.
func (e *Engine) Lookup(workerID string) (corebus.Identity, bool) {
	return e.registry.Lookup(workerID)
}

// publishSendAllowed reports whether from may directed-send an event of typ to
// the given target worker, per its PublishAllow grants.
func (e *Engine) publishSendAllowed(from string, typ event.EventType, target string) bool {
	id, ok := e.registry.Lookup(from)
	if !ok {
		return false
	}
	for _, p := range id.PublishAllow {
		if p.SendAllowed(typ, target) {
			return true
		}
	}
	return false
}

// publishBroadcastAllowed reports whether from may broadcast an event of typ,
// per its PublishAllow grants.
func (e *Engine) publishBroadcastAllowed(from string, typ event.EventType) bool {
	id, ok := e.registry.Lookup(from)
	if !ok {
		return false
	}
	for _, p := range id.PublishAllow {
		if p.BroadcastAllowed(typ) {
			return true
		}
	}
	return false
}

// patternMatchesAny reports whether a routed event matches any of a worker's
// subscription patterns. Matching delegates to event.EventPattern.Matches — the
// single source of truth for subscription matching — so the bus and workers
// agree on both type and source matching.
func patternMatchesAny(evt event.Event, patterns []event.EventPattern) bool {
	for _, p := range patterns {
		if p.Matches(evt) {
			return true
		}
	}
	return false
}
