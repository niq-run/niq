// The capability registry: reason worker's single extension point.
//
// A reason worker responds to events and processes them; the capability
// registry makes that uniform. Registering a capability declares an event the
// worker responds to (which flows into worker.ready's "watch") and binds a
// handler that executes when the event arrives on the bus. The event type
// carries the capability kind: tool.request → tool (LLM-facing, loop-back),
// worker.update → meta update, worker.query → meta query — and any event type
// can be registered (the base channels are just the base's own registrations).
//
// Capabilities carry no LLM-facing marks (no exposure flag, no LLM tool name).
// The tool-list builder decides, by its own policy, which discovered
// capabilities its LLM sees and under what names.
package reason

import (
	"strings"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
)

// Capability describes an event a reason worker responds to.
type Capability struct {
	// Event is the event type the capability responds to. Any event type is
	// allowed; tool.request / worker.update / worker.query are the base
	// channels registered by BaseReasonWorker itself.
	Event event.EventType
	// KeyField is the payload field holding the discriminator (e.g. "name" for
	// tool.request, "op" for worker.update, "subject" for worker.query). Empty
	// means the capability matches the event type alone.
	KeyField string
	// Key is the value KeyField must equal. Empty when KeyField is empty.
	Key string

	Description string
	Parameters  map[string]any
}

// CapabilityHandler executes when the registered event arrives on the bus.
// The handler is a pure closure — it captures whatever state it needs (the
// base worker, or the embedding reason-family worker's own fields), so the
// signature carries no base-type dependency.
type CapabilityHandler func(evt event.Event)

// registeredCapability is a capability bound to its handler.
type registeredCapability struct {
	cap     Capability
	handler CapabilityHandler
}

// The registry key is built by capRegistryKey, which joins the three fields
// with the NUL byte (\x00, ASCII 0). NUL is chosen as the separator because it
// never occurs in event type / field / key identifiers, so two distinct
// capabilities can never collide — and it is an internal-only key, never
// surfaced to logs or the user, so its invisibility is harmless.
func capRegistryKey(cap Capability) string {
	return string(cap.Event) + "\x00" + cap.KeyField + "\x00" + cap.Key
}

// Register binds a capability to a handler. Registering the same
// (Event, KeyField, Key) replaces the previous registration. Register is
// expected before Start (a reason-family worker's constructor), so the watch
// declaration and subscriptions can be derived from the registry.
func (w *BaseReasonWorker) Register(cap Capability, h CapabilityHandler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.capabilities == nil {
		w.capabilities = make(map[string]registeredCapability)
	}
	w.capabilities[capRegistryKey(cap)] = registeredCapability{cap: cap, handler: h}
}

// dispatchCapability routes an event to the registered capability matching
// its event type and discriminator (KeyField == Key). Returns whether a
// handler ran.
func (w *BaseReasonWorker) dispatchCapability(evt event.Event) bool {
	for _, rc := range w.capabilities {
		if rc.cap.Event != evt.Type {
			continue
		}
		if rc.cap.KeyField != "" {
			v, _ := evt.Payload[rc.cap.KeyField].(string)
			if v != rc.cap.Key {
				continue
			}
		}
		rc.handler(evt)
		return true
	}
	return false
}

// capToolName is the default LLM-facing name for an own capability: its
// discriminator with dots → underscores (context.compress → context_compress).
// The default tool-list builder names exposed capabilities with it, and the
// reason worker reverse-maps LLM tool calls back to meta capabilities with the
// same convention, so a custom builder that exposes a meta capability must keep
// this name for the meta routing to recognize it.
func capToolName(cap Capability) string {
	return strings.ReplaceAll(cap.Key, ".", "_")
}

// capabilityByToolName finds a registered capability by its default LLM-facing
// tool name (capToolName). Used to route LLM tool calls: a meta capability is
// triggered by converting the tool call back into its worker.update/worker.query
// event; an own tool capability loops back as tool.request.
func (w *BaseReasonWorker) capabilityByToolName(name string) (Capability, bool) {
	for _, rc := range w.capabilities {
		if capToolName(rc.cap) == name {
			return rc.cap, true
		}
	}
	return Capability{}, false
}

// DiscoveredCap is a capability as seen on the bus: which worker declared it
// and the event it responds to. It is the unified unit the tool-list builder
// operates on — the worker's own registered capabilities and the capabilities
// discovered from peers are presented uniformly, distinguished only by Source.
type DiscoveredCap struct {
	Source      string          // declaring worker ID (own ID for self)
	Event       event.EventType // tool.request / worker.update / worker.query / custom
	KeyField    string
	Key         string
	Description string
	Parameters  map[string]any
}

// discoveredCapabilities returns the unified capability universe: every
// capability this worker knows about, its own included, in one list. It is fed
// by every worker.ready announcement — the self-directed one included — so the
// builder sees one bus-derived view, no two-source assembly.
func (w *BaseReasonWorker) discoveredCapabilities() []DiscoveredCap {
	return w.discovered
}

// llmToolDefs builds the LLM tool list via the worker's tool-list builder
// over the discovered capability universe.
func (w *BaseReasonWorker) llmToolDefs() []llm.ToolDef {
	return w.toolListBuilder(w, w.discoveredCapabilities())
}

