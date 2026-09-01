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
	"sync"

	"github.com/niq-run/niq/core/event"
)

// Extension describes an event a worker responds to. It is how a worker is
// extended: each registration teaches the base to recognize one more
// (event, discriminator) pair and answer it with a handler.
type Extension struct {
	// Event is the event type the extension responds to — its identity. Any
	// event type is allowed; invoking the extension means sending this event.
	Event event.EventType
	// KeyField is the payload field holding the discriminator when several
	// extensions multiplex on one event type. Empty means the extension is
	// identified by the event type alone.
	KeyField string
	// Key is the value KeyField must equal. Empty when KeyField is empty.
	Key string
	// SelfOnly marks an extension that only the declaring worker itself serves
	// (e.g. send_message, list_workers — operations on that worker's own view
	// of the bus). Such extensions are left out of the peer-facing
	// worker.ready, so peers never see them as callable tools; the worker
	// discovers them from its own registry instead.
	SelfOnly bool

	Description string
	Parameters  map[string]any
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

// The registry key is built by extensionKey, which joins the three fields
// with the NUL byte (\x00, ASCII 0). NUL is chosen as the separator because it
// never occurs in event type / field / key identifiers, so two distinct
// extensions can never collide — and it is an internal-only key, never
// surfaced to logs or the user, so its invisibility is harmless.
func extensionKey(ext Extension) string {
	return string(ext.Event) + "\x00" + ext.KeyField + "\x00" + ext.Key
}

// extensionRegistry is the synchronized store behind BaseWorker. It is held
// behind a pointer so BaseWorker stays copyable by value (no lock in the
// struct itself); copies share one registry.
type extensionRegistry struct {
	mu   sync.RWMutex
	regs map[string]registeredExtension
}

// Register binds an extension to a handler. Registering the same
// (Event, KeyField, Key) replaces the previous registration. Safe at any
// time — before Start (in a worker's constructor) or at runtime.
func (w *BaseWorker) Register(ext Extension, h ExtensionHandler) {
	if w.extensions == nil {
		w.extensions = &extensionRegistry{regs: make(map[string]registeredExtension)}
	}
	w.extensions.mu.Lock()
	defer w.extensions.mu.Unlock()
	w.extensions.regs[extensionKey(ext)] = registeredExtension{ext: ext, handler: h}
}

// DispatchExtension routes an event to the registered extension matching its
// event type and discriminator (KeyField == Key). Returns whether a handler
// ran. The handler is looked up under the registry lock but invoked outside
// it, so a handler may register further extensions.
func (w *BaseWorker) DispatchExtension(evt event.Event) bool {
	if w.extensions == nil {
		return false
	}
	w.extensions.mu.RLock()
	var h ExtensionHandler
	for _, r := range w.extensions.regs {
		if r.ext.Event != evt.Type {
			continue
		}
		if r.ext.KeyField != "" {
			v, _ := evt.Payload[r.ext.KeyField].(string)
			if v != r.ext.Key {
				continue
			}
		}
		h = r.handler
		break
	}
	w.extensions.mu.RUnlock()
	if h == nil {
		return false
	}
	h(evt)
	return true
}

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

// watchEntry renders one extension into the worker.ready "watch" wire entry:
// its event type identifies the capability; a KeyField, when present, folds
// the discriminator into the parameters (a shared event multiplexing several
// capabilities). The wire field is literally "watch" — see announce.go in
// pkg/reason for the naming note.
func (w *BaseWorker) watchEntry(ext Extension) map[string]any {
	entry := map[string]any{"event": string(ext.Event), "desc": ext.Description}
	if ext.KeyField != "" {
		params := cloneMap(ext.Parameters)
		params[ext.KeyField] = ext.Key
		entry["parameters"] = params
	} else if len(ext.Parameters) > 0 {
		entry["parameters"] = ext.Parameters
	}
	return entry
}

// WatchEntries renders the registry into the worker.ready "watch" wire format.
// includeSelfOnly controls whether SelfOnly extensions are rendered: a
// peer-facing broadcast leaves them out (they are not callable by peers),
// while a worker's own view includes them.
func (w *BaseWorker) WatchEntries(includeSelfOnly bool) []map[string]any {
	caps := w.Extensions()
	out := make([]map[string]any, 0, len(caps))
	for _, e := range caps {
		if e.SelfOnly && !includeSelfOnly {
			continue
		}
		out = append(out, w.watchEntry(e))
	}
	return out
}

// AnnounceReady broadcasts this worker's presence on the bus: the externally
// callable contract (SelfOnly left out), self excluded. publishes is the wire
// form of the events this worker declares it emits (worker.ready "publishes"),
// nil if it publishes none. This is the generic announcement every worker uses
// to tell PEERS what it serves — it is not for self, so there is no
// self-directed half here. The reason worker additionally sends itself its
// full contract (including SelfOnly) so it can learn its own capabilities
// through the same discovery pipeline as everyone else; that is its own extra,
// not part of this helper.
func (w *BaseWorker) AnnounceReady(workerType string, publishes []map[string]any) {
	payload := map[string]any{
		"worker_id": w.ID(),
		"type":      workerType,
		"watch":     w.WatchEntries(false),
	}
	if len(publishes) > 0 {
		payload["publishes"] = publishes
	}
	presence := event.New(event.TypeWorkerReady, w.ID(), payload)
	presence.ExcludeWorkerID = w.ID()
	_ = w.Channel.Broadcast(context.Background(), presence)
}

func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
