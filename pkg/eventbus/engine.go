package eventbus

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

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
}

// PersistStats returns cumulative persistence counters for observation.
func (e *Engine) PersistStats() (total, dropped uint64) {
	return e.persistTotal.Load(), e.persistDropped.Load()
}

// NewEngine creates a new Engine.
// store may be nil; if nil, events are not persisted.
func NewEngine(registry corebus.IdentityRegistry, store store.AppendStore) *Engine {
	return &Engine{
		channels: make(map[string]corebus.BusSideChannel),
		registry: registry,
		store:    store,
	}
}

// Connect registers a worker as online by associating its channel.
// The worker must have a registered identity.
func (e *Engine) Connect(workerID string, ch corebus.BusSideChannel) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, ok := e.registry.Lookup(workerID); !ok {
		return fmt.Errorf("eventbus: cannot connect %s: identity not registered", workerID)
	}
	if _, ok := e.channels[workerID]; ok {
		return fmt.Errorf("eventbus: worker %s already connected", workerID)
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

// broadcastGone advertises a worker's departure as a worker.gone broadcast.
// from is the departed worker; the payload names it so reason's discovery can
// age it out. Best-effort. Routing reuses HandleRequest so subscription
// matching, persistence and onEvent observers all apply.
func (e *Engine) broadcastGone(ctx context.Context, workerID string) {
	evt := event.New(event.TypeWorkerGone, workerID, map[string]any{
		"worker_id": workerID,
	})
	// The departed worker is excluded from its own broadcast (it cannot
	// receive anyway — it is offline), but set it so a straggler reconnecting
	// under the same id does not observe its own gone event.
	evt.ExcludeWorkerID = workerID
	e.HandleRequest(ctx, corebus.Request{Type: corebus.RequestBroadcast, Events: []event.Event{evt}}, workerID)
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

		for _, target := range req.Targets {
			ch, ok := e.channels[target]
			if !ok {
				log.Printf("[eventbus] send: target %s not online (from %s)", target, from)
				continue
			}
			if err := ch.Send(ctx, evt); err != nil {
				log.Printf("[eventbus] send: deliver to %s failed: %v", target, err)
			}
		}

		evt.Recipients = req.Targets
		e.persistEvent(ctx, evt)
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
		// Preserve the trace_id the sender stamped on the event when the request
		// carries none (the in-process transport never sets req.TraceID).
		if req.TraceID != "" {
			evt.TraceID = req.TraceID
		}

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
	if e.store != nil && !evt.Transient {
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
