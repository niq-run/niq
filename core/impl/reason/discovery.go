// Bus discovery: what this worker has learned about the other workers on the
// bus, and the callable capabilities derived from that.
//
// This is the INBOUND half of presence — it consumes worker.ready /
// worker.gone and maintains the discovered universe (w.discovered): the single
// source of truth for what this worker can call. The OUTBOUND half — announcing
// this worker's own contract — lives in announce.go. The extension registry —
// what this worker itself declares and how it answers — lives in pkg/baseworker.
package reason

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// DiscoveredCapability is a capability as seen on the bus: which worker declared it
// and the event it responds to. It is the unified unit the tool-list builder
// operates on — this worker's own contract (learned from its self-directed
// worker.ready) and every peer's presence broadcast are presented uniformly,
// distinguished only by Source.
//
// This is the bus-discovered universe, NOT the worker's own declared registry:
// BaseReasonWorker learns it by observing worker.ready / worker.gone, so it
// describes what the worker can SEE, not what it announces. The self-registry
// (Extension / Register) lives in pkg/baseworker.
type DiscoveredCapability struct {
	Source      string          // declaring worker ID (own ID for self)
	Event       event.EventType // the extension's own event type (send_message, context.compress, ...)
	Description string
	Parameters  map[string]any
}

// discoveredCapabilities returns the unified capability universe: every
// capability this worker knows about, its own included, in one list. It is fed
// by every worker.ready announcement — the worker's own directed full contract
// and every peer's presence broadcast — so the builder sees one bus-derived
// view, no two-source assembly.
func (w *BaseReasonWorker) discoveredCapabilities() []DiscoveredCapability {
	return w.discovered
}

// HandleWorkerReady learns a worker's capabilities from its worker.ready
// announcement, feeding the discovered universe (w.discovered). Each ready
// event carries that worker's complete contract (the worker excludes itself
// from its own presence broadcast and sends its full contract to itself), so
// the source's previous view is replaced wholesale — which is also what lets a
// re-announcement update a worker's capabilities.
func (w *BaseReasonWorker) HandleWorkerReady(evt event.Event) {
	workerID := evt.WorkerId
	if workerID == "" {
		return
	}

	// A reason's ready lists the events it RESPONDS to, not callable tools: this
	// worker exposes its own capabilities to itself, but a peer reason's ready is
	// not treated as callable capabilities — its extensions (context.compress,
	// program_query, ...) are never surfaced as tools here. Infra workers
	// (workspace/timer/program/directory...) expose real tools and are kept.
	if workerID != w.ID() && evt.Payload["type"] == "reason" {
		log.Printf("[reason %s] ignoring peer reason %s ready (not a callable tool)", w.ID(), workerID)
		return
	}

	// Re-announcement is idempotent: drop this worker's previous view.
	w.discovered = removeDiscoveredCapabilitys(w.discovered, workerID)

	// Parse the capability contract (watch) into the discovery universe.
	if raw, ok := evt.Payload["watch"]; ok {
		if b, err := json.Marshal(raw); err == nil {
			var watchRaw []map[string]any
			if err := json.Unmarshal(b, &watchRaw); err == nil {
				for _, e := range watchRaw {
					if dc, ok := discoveredFromWatch(workerID, e); ok {
						w.discovered = append(w.discovered, dc)
					}
				}
				if len(watchRaw) > 0 {
					log.Printf("[reason %s] received watch from %s (%d entries)", w.ID(), workerID, len(watchRaw))
				}
			}
		}
	}
}

// discoveredFromWatch parses a worker.ready "watch" entry into a
// DiscoveredCapability. The event type is the capability's identity; the entry
// carries an optional description and parameter schema.
func discoveredFromWatch(source string, e map[string]any) (DiscoveredCapability, bool) {
	typ, _ := e["event"].(string)
	if typ == "" {
		return DiscoveredCapability{}, false
	}
	dc := DiscoveredCapability{
		Source: source,
		Event:  event.EventType(typ),
	}
	dc.Description, _ = e["desc"].(string)
	dc.Parameters, _ = e["parameters"].(map[string]any)
	return dc, true
}

// removeDiscoveredCapabilitys returns caps without the entries declared by source.
func removeDiscoveredCapabilitys(caps []DiscoveredCapability, source string) []DiscoveredCapability {
	out := caps[:0]
	for _, c := range caps {
		if c.Source != source {
			out = append(out, c)
		}
	}
	return out
}

// handleWorkerGone forgets a departed worker's capabilities.
func (w *BaseReasonWorker) handleWorkerGone(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)
	if workerID == "" {
		return
	}

	w.discovered = removeDiscoveredCapabilitys(w.discovered, workerID)
	log.Printf("[reason %s] forgot capabilities from %s", w.ID(), workerID)
}

// encodeToolName maps a Tool to its LLM-facing name. A worker's own tools
// (provider empty or this worker) keep a bare name (with inner '.' -> '_');
// tools backed by another worker become provider__name, so the worker/tool
// boundary is unambiguous. Invariant: worker IDs and tool names must not
// contain "__" (double underscore); '__' is the separator.
func encodeToolName(w *BaseReasonWorker, t worker.Tool) string {
	p, n := t.Provider, t.Name
	if p == "" || p == w.ID() {
		return strings.ReplaceAll(n, ".", "_")
	}
	return p + "__" + strings.ReplaceAll(n, ".", "_")
}

// timeoutTool is the bare-name contract for the built-in timeout: if any peer
// provides a capability whose bare name is "timeout", the reason worker treats
// it as this round's tool-call timeout. Provider-agnostic — the timer worker
// need not be named "timer". If no peer provides it, the feature is disabled.
const timeoutTool = "timeout"

// timeoutProvider returns the peer worker providing the built-in timeout
// capability, when name is that capability's LLM-facing tool name (e.g.
// "timer__timeout"). Convention-based and provider-agnostic: any peer whose
// capability has the bare event "timeout" qualifies, so the timer worker need
// not be named "timer". Built on the generic capabilityByToolName lookup.
func (w *BaseReasonWorker) timeoutProvider(name string) (string, bool) {
	cap, ok := w.capabilityByToolName(name)
	if !ok || cap.Source == w.ID() || string(cap.Event) != timeoutTool {
		return "", false
	}
	return cap.Source, true
}

// elapseTool is the bare-name contract for a one-shot reminder capability: the
// peer publishing a capability named "elapse" fires the rate-limit retry
// reminder. Provider-agnostic like timeoutTool — the worker need not be named
// "timer".
const elapseTool = "elapse"

// peerCapability returns the peer worker exposing the capability with the given
// bare event name (the name it is addressed by on the bus), or false if no peer
// offers it. Own capabilities never qualify. Callers must hold w.mu. It lets the
// mechanism reach a peer's capability by its name rather than a hardcoded
// worker id.
func (w *BaseReasonWorker) peerCapability(bareEvent string) (string, bool) {
	for _, cap := range w.discovered {
		if cap.Source != w.ID() && string(cap.Event) == bareEvent {
			return cap.Source, true
		}
	}
	return "", false
}
