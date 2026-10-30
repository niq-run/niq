// The extension registry: the uniform mechanism for what a worker responds to
// and how. A worker is extended by what it responds to and how — extensions
// are how.
//
// A worker responds to events and processes them; the registry makes that
// uniform. Registering an extension declares an event the worker responds to
// (which flows into worker.ready's "watch") and binds a handler that executes
// when the event arrives on the bus. The event type carries the extension
// kind: a worker's own tool invocations loop back on their own event type
// (e.g. send_message, context.compress, provider.*), and any event type can
// be registered.
//
// Registration is safe at any time — before Start (in a worker's constructor)
// or at runtime. The registry is internally synchronized; handlers are looked
// up under the lock but invoked OUTSIDE it, so a handler may register further
// extensions without deadlocking.
//
// This registry is generic bus machinery: it knows nothing about LLMs. The
// LLM-facing naming conventions built on top of it live in pkg/reason. The
// discovery-side view of what other workers offer is pkg/reason's
// DiscoveredCapability — the same fact, seen from outside.
package baseworker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/niq-run/niq/core/itfs/event"
)

// Extension describes an event a worker responds to. It is how a worker is
// extended: each registration teaches the base to recognize one more event
// and answer it with a handler. The event type IS the extension's identity —
// there is no separate discriminator; distinct things are distinct events.
type Extension struct {
	// Event is the event type the extension responds to — its identity. Any
	// event type is allowed; invoking the extension means sending this event.
	Event event.EventType
	// Internal marks an extension that belongs to the worker's OWN view: it is
	// not advertised in the peer-facing catalogue (worker.ready watch ->
	// directory -> WebUI / PTC), and a request from any other worker is refused
	// before the handler runs (a request.rejected when the event carries a
	// request id). The declaring worker still invokes it normally through the
	// bus, and the self-directed ready keeps it in its own tool list.
	//
	// This is a scope rule, not a general access control: it says the capability
	// is meaningful only to its owner. Whether another worker may address this
	// one at all is still the bus ACL's job.
	Internal bool

	Description string
	Parameters  map[string]any
	// Response describes the SHAPE of the reply (a JSON Schema object), when the
	// capability has one — the response half of the request/response pair, where
	// Parameters describes the request. Documentation only: nothing validates a
	// reply. It exists so an agent can plan how to parse a reply BEFORE calling —
	// which is what makes a PTC script writable in one shot — and it rides the
	// advertised contract (WatchEntries -> worker.ready -> the directory roster).
	//
	// It describes what the CALLER receives: the value of the reply's "result"
	// field, which keeps its type (see ReplyCompleted) — a string for a prose
	// reply, an object/array/number for a structured one. So
	// {type: object, properties: …} means "result IS an object with these keys".
	// A reply that is an OBJECT is sent as the payload itself; a non-object reply
	// (text, number, array) is wrapped as {"result": value}. "content" and
	// "result" are therefore reserved top-level keys of a reply payload.
	//
	// Optional: a capability whose reply is prose leaves this nil. A structured
	// reply (a JSON list, an object with fixed keys) should declare it.
	Response map[string]any
}

// ExtensionHandler executes when the registered event arrives on the bus.
// The handler is a pure closure — it captures whatever state it needs (the
// base worker, or the embedding worker's own fields), so the signature
// carries no base-type dependency.
type ExtensionHandler func(evt event.Event)

// registeredExtension is an extension bound to its handler.
type registeredExtension struct {
	ext     Extension
	handler ExtensionHandler
}

// The registry key is built by extensionKey from the event type with a NUL
// byte suffix (\x00, ASCII 0). NUL is chosen because it never occurs in an
// event-type identifier, so two distinct extensions can never collide — and it
// is an internal-only key, never surfaced to logs or the user.
func extensionKey(ext Extension) string {
	return string(ext.Event) + "\x00"
}

// extensionRegistry is the synchronized store behind BaseWorker. It is held
// behind a pointer so BaseWorker stays copyable by value (no lock in the
// struct itself); copies share one registry.
type extensionRegistry struct {
	mu   sync.RWMutex
	regs map[string]registeredExtension
}

// Register binds an extension to a handler. Registering the same event type
// replaces the previous registration. Safe at any time — before Start (in a
// worker's constructor) or at runtime.
func (w *BaseWorker) Register(ext Extension, h ExtensionHandler) {
	if w.extensions == nil {
		w.extensions = &extensionRegistry{regs: make(map[string]registeredExtension)}
	}
	w.extensions.mu.Lock()
	defer w.extensions.mu.Unlock()
	w.extensions.regs[extensionKey(ext)] = registeredExtension{ext: ext, handler: h}
}

// Unregister removes the extensions bound to the given event types. Removing an
// event that was never registered is a no-op. Safe at any time, like Register;
// it is how a worker withdraws capabilities it registered dynamically.
func (w *BaseWorker) Unregister(events ...event.EventType) {
	if w.extensions == nil {
		return
	}
	w.extensions.mu.Lock()
	defer w.extensions.mu.Unlock()
	for _, evt := range events {
		delete(w.extensions.regs, extensionKey(Extension{Event: evt}))
	}
}

// DispatchExtension routes an event to the registered extension matching its
// event type. Returns whether a handler ran. The handler is looked up under the
// registry lock but invoked outside it, so a handler may register further
// extensions.
//
// An Internal extension is self-scoped: a request from any other worker is
// refused before the handler runs (request.rejected, or just logged when the
// event is a notification). The bus stamps the sender's worker id from its
// credential, so a peer cannot pose as the owner.
//
// A request that matches no extension is answered with request.failed rather
// than dropped: the caller's pending call would otherwise wait forever on a
// worker that simply does not serve that event.
func (w *BaseWorker) DispatchExtension(evt event.Event) bool {
	if w.extensions == nil {
		w.replyNoHandler(evt)
		return false
	}
	w.extensions.mu.RLock()
	reg, ok := w.extensions.regs[extensionKey(Extension{Event: evt.Type})]
	w.extensions.mu.RUnlock()
	if !ok {
		w.replyNoHandler(evt)
		return false
	}
	if reg.ext.Internal && evt.WorkerId != w.id {
		if evt.RequestId != "" && w.Channel != nil && !isReplyEvent(evt.Type) {
			w.ReplyRejected(evt.WorkerId, evt.RequestId,
				"internal capability: only "+w.id+" may invoke it", evt.TraceID)
		}
		log.Printf("[baseworker %s] refused internal capability %s from %s",
			w.id, evt.Type, evt.WorkerId)
		return true
	}
	reg.handler(evt)
	return true
}

// isReplyEvent reports whether an event type ANSWERS a request rather than
// asking one. A reply carries the requester's RequestId like any other directed
// event, which is why the distinction has to be made by type: without it a
// worker that receives a failure reply it has no handler for answers it with
// another failure reply, and two such workers bounce error events forever.
//
// request.cancel is deliberately NOT here: the caller sends it TO a worker to
// abandon a pending call, so it may legitimately be rejected or fail.
func isReplyEvent(t event.EventType) bool {
	switch t {
	case event.TypeRequestCompleted, event.TypeRequestFailed,
		event.TypeRequestRejected, event.TypeRequestProgressed:
		return true
	}
	return false
}

// replyNoHandler answers a request for an event this worker does not serve, so
// the caller's pending call cannot hang on a silent drop. Only requests get a
// reply — a notification (no request id) has nothing waiting on it.
//
// The reply names the worker and then lists what it DOES serve: "no handler for
// event x" leaves the caller to guess whether the event name was wrong, the
// worker was wrong, or the capability lives somewhere else. The list is the
// caller-visible contract (Internal events only for the owner), so what it shows
// is exactly what that caller may send.
func (w *BaseWorker) replyNoHandler(evt event.Event) {
	// A reply is never a request: nothing is waiting for an answer to it, so
	// there is nothing to answer. Replying anyway is how two workers that both
	// lack a handler end up feeding each other's failure replies — once seen
	// running at ~1900 events/second for ten minutes and writing 1.17M rows.
	if isReplyEvent(evt.Type) {
		return
	}
	if evt.RequestId == "" || w.Channel == nil {
		return
	}
	names := visibleEventNames(w.Extensions(), evt.WorkerId == w.id)
	served := "(none)"
	if len(names) > 0 {
		if len(names) > maxServedEventsInError {
			names = append(names[:maxServedEventsInError:maxServedEventsInError], "…")
		}
		served = strings.Join(names, ", ")
	}
	w.ReplyFailed(evt.WorkerId, evt.RequestId,
		fmt.Sprintf("%s does not serve event %q; it serves: %s", w.id, evt.Type, served), evt.TraceID)
}

// maxServedEventsInError caps the event list in a "does not serve" reply: the
// message is a pointer, not a catalogue (a ready-mode mcp worker can carry
// dozens of tool events).
const maxServedEventsInError = 10

// Extensions returns a snapshot of the registered extensions. The
// worker.ready announcement renderer and any lookup helpers read the registry
// through it.
func (w *BaseWorker) Extensions() []Extension {
	if w.extensions == nil {
		return nil
	}
	w.extensions.mu.RLock()
	defer w.extensions.mu.RUnlock()
	out := make([]Extension, 0, len(w.extensions.regs))
	for _, r := range w.extensions.regs {
		out = append(out, r.ext)
	}
	return out
}

// WatchEntries renders the registry into the worker.ready "watch" wire format:
// one entry per extension, {"event", "desc", "parameters" (when declared)}. The
// event type identifies the capability; parameters are its argument schema. The
// wire field is literally "watch" — see capability.go in pkg/reason for the
// naming note.
//
// includeInternal controls whether Internal extensions are rendered: a
// peer-facing broadcast leaves them out (they are not advertised to peers),
// while a worker's own view includes them. Visibility only — dispatch is gated
// by the bus ACL, not by this parameter.
func (w *BaseWorker) WatchEntries(includeInternal bool) []map[string]any {
	caps := w.Extensions()
	out := make([]map[string]any, 0, len(caps))
	for _, e := range caps {
		if e.Internal && !includeInternal {
			continue
		}
		entry := map[string]any{"event": string(e.Event), "desc": e.Description}
		if len(e.Parameters) > 0 {
			entry["parameters"] = e.Parameters
		}
		if len(e.Response) > 0 {
			entry["response"] = e.Response
		}
		out = append(out, entry)
	}
	return out
}

// AnnounceReady broadcasts this worker's presence on the bus: the externally
// callable contract (Internal left out), self excluded. publishes is the wire
// form of the events this worker declares it emits (worker.ready "publishes"),
// nil if it publishes none. This is the generic announcement every worker uses
// to tell PEERS what it serves — it is not for self, so there is no
// self-directed half here. The reason worker additionally sends itself its
// full contract (including Internal) so it can learn its own capabilities
// through the same discovery pipeline as everyone else; that is its own extra,
// not part of this helper.
func (w *BaseWorker) AnnounceReady(workerType string, publishes []map[string]any) {
	payload := map[string]any{
		"type":  workerType,
		"watch": w.WatchEntries(false),
	}
	if len(publishes) > 0 {
		payload["publishes"] = publishes
	}
	presence := event.New(event.TypeWorkerReady, w.ID(), payload)
	presence.ExcludeWorkerID = w.ID()
	presence.Transient = true // presence: delivered live, not durable history
	_ = w.Channel.Broadcast(context.Background(), presence)
}

// AnnounceReadyTo sends this worker's presence (worker.ready) directly to a
// single target, instead of broadcasting it. It is the reply to a
// worker.discover: the discoverer asked who the fleet is, so each responder
// answers just the asker — the complete answer the discoverer needs, at O(N)
// total directed sends instead of an O(N²) broadcast re-announcement storm
// (N workers each re-broadcasting to N subscribers).
//
// includeInternal mirrors WatchEntries: false sends the peer-facing contract
// (Internal left out, for replying to a peer discover); true sends the full
// contract including Internal (for a worker learning its own capabilities).
func (w *BaseWorker) AnnounceReadyTo(target, workerType string, publishes []map[string]any, includeInternal bool) {
	payload := map[string]any{
		"type":  workerType,
		"watch": w.WatchEntries(includeInternal),
	}
	if len(publishes) > 0 {
		payload["publishes"] = publishes
	}
	ready := event.New(event.TypeWorkerReady, w.ID(), payload)
	ready.Transient = true // presence: delivered live, not durable history
	_ = w.Channel.Send(context.Background(), ready, target)
}
