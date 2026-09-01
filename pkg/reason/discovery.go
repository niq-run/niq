// Bus discovery: what this worker has learned about the other workers on the
// bus, and the callable-tool index derived from that.
//
// This is the INBOUND half of presence — it consumes worker.ready /
// worker.gone and maintains the unified discovered universe (w.discovered,
// w.publishMap) plus the dispatch table (w.tools) derived from
// it. The OUTBOUND half — announcing this worker's own contract — lives in
// announce.go. The extension registry — what this worker itself declares and
// how it answers — lives in pkg/baseworker.
//
// The discovered universe is the single source of truth: the dispatch table is
// rebuilt from it on every change, never maintained separately.
package reason

import (
	"encoding/json"
	"log"
	"strings"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
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
	Event       event.EventType // tool.request / worker.update / worker.query / custom
	KeyField    string
	Key         string
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

// DiscoveredWorker is one worker in the directory served by list_workers: the
// tools and published events this worker has learned about it from the bus.
// It is NOT a snapshot of this worker's own state — that is the separate
// Snapshot/Restore lifecycle.
type DiscoveredWorker struct {
	WorkerID  string         `json:"worker_id"`
	Tools     []worker.Tool  `json:"tools,omitempty"`
	Publishes []EventPublish `json:"publishes,omitempty"`
}

// DiscoveredWorkers aggregates the discovered tools and published events by
// provider — the payload behind list_workers. It is a read-only view of the
// bus-discovered state, not a worker strategy and not this worker's own state
// Snapshot: the list_workers extension (pkg/worker/reason) calls it and sends
// the result.
func (w *BaseReasonWorker) DiscoveredWorkers() []DiscoveredWorker {
	providers := make(map[string]*DiscoveredWorker)
	for _, tool := range w.tools {
		info, ok := providers[tool.Provider]
		if !ok {
			info = &DiscoveredWorker{WorkerID: tool.Provider, Publishes: w.publishMap[tool.Provider]}
			providers[tool.Provider] = info
		}
		info.Tools = append(info.Tools, tool)
	}
	for provider, events := range w.publishMap {
		if _, ok := providers[provider]; !ok {
			providers[provider] = &DiscoveredWorker{WorkerID: provider, Publishes: events}
		}
	}
	out := make([]DiscoveredWorker, 0, len(providers))
	for _, info := range providers {
		out = append(out, *info)
	}
	return out
}

// HandleWorkerReady learns a worker's capabilities and published events from
// its worker.ready announcement, feeding the unified discovery universe
// (discovered) and the tool table used for dispatch. Each ready event carries
// that worker's complete contract (the worker excludes itself from its own
// presence broadcast and sends its full contract to itself), so the source's
// previous view is replaced wholesale — which is also what lets a
// re-announcement update a worker's capabilities.
func (w *BaseReasonWorker) HandleWorkerReady(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)
	if workerID == "" {
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

	// Parse publishes.
	if b, err := json.Marshal(evt.Payload["publishes"]); err == nil {
		var eventsRaw []EventPublish
		if err := json.Unmarshal(b, &eventsRaw); err == nil && len(eventsRaw) > 0 {
			w.publishMap[workerID] = eventsRaw
			log.Printf("[reason %s] received %d event(s) from %s", w.ID(), len(eventsRaw), workerID)
		}
	}

	// The dispatch table is a derived index over the discovery universe; keep
	// it in step with what was just learned/removed.
	w.rebuildDispatchTable()
}

// discoveredFromWatch parses a worker.ready "watch" entry into a DiscoveredCapability.
// tool.request entries carry the tool name top-level; worker.update / worker.query
// entries fold the discriminator (op / subject) into parameters.
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
	if name, ok := e["name"].(string); ok && name != "" {
		dc.KeyField, dc.Key = "name", name
	} else if dc.Parameters != nil {
		if op, ok := dc.Parameters["op"].(string); ok && op != "" {
			dc.KeyField, dc.Key = "op", op
		} else if subj, ok := dc.Parameters["subject"].(string); ok && subj != "" {
			dc.KeyField, dc.Key = "subject", subj
		}
	}
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

// handleWorkerGone forgets a departed worker's capabilities, tools and
// published events.
func (w *BaseReasonWorker) handleWorkerGone(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)

	w.discovered = removeDiscoveredCapabilitys(w.discovered, workerID)
	delete(w.publishMap, workerID)
	// The dispatch table is a derived index; drop the departed worker's tools
	// the same way as everything else — by rebuilding from discovered.
	w.rebuildDispatchTable()
	log.Printf("[reason %s] removed tools and events from %s", w.ID(), workerID)
}

// rebuildDispatchTable derives the dispatch table (w.tools) from
// the discovery universe, so the two stay in step and the discovery universe is
// the single source of truth. Only peer tool.request capabilities become
// callable tools: own capabilities dispatch through the registry instead, so
// they are intentionally not indexed here. Called whenever discovered changes
// (HandleWorkerReady / handleWorkerGone).
func (w *BaseReasonWorker) rebuildDispatchTable() {
	for name := range w.tools {
		delete(w.tools, name)
	}
	for _, cap := range w.discovered {
		if cap.Source == w.ID() {
			continue // own capabilities dispatch through the registry
		}
		id := string(cap.Event)
		if cap.KeyField != "" {
			id = cap.Key
		}
		t := worker.Tool{Name: id, Description: cap.Description,
			Parameters: cap.Parameters, Provider: cap.Source}
		t.Name = encodeToolName(w, t)
		w.tools[t.Name] = t
	}
}

// allTools returns every tool in the dispatch table (all peer-declared
// tool.request capabilities, under their encoded LLM-facing names).
func (w *BaseReasonWorker) allTools() []worker.Tool {
	tools := make([]worker.Tool, 0, len(w.tools))
	for _, t := range w.tools {
		tools = append(tools, t)
	}
	return tools
}

// encodeToolName maps a Tool to the name both the w.tools table and the LLM
// use. A worker's own tools (provider empty or this worker) keep a bare name
// (with inner '.' -> '_'); tools backed by another worker become provider__name, so
// the worker/tool boundary is unambiguous. Invariant: worker IDs and tool
// names must not contain "__" (double underscore); '__' is the separator.
func encodeToolName(w *BaseReasonWorker, t worker.Tool) string {
	p, n := t.Provider, t.Name
	if p == "" || p == w.ID() {
		return strings.ReplaceAll(n, ".", "_")
	}
	return p + "__" + strings.ReplaceAll(n, ".", "_")
}

// bareToolName reverses tool encoding to the bare name. For an external tool
// (provider__name) it strips the prefix; a worker's own tools (no provider
// prefix) return name unchanged.
func bareToolName(t worker.Tool) string {
	if t.Provider == "" {
		return t.Name
	}
	return strings.TrimPrefix(t.Name, t.Provider+"__")
}

// timeoutTool is the bare-name contract for the tool built-in timeout:
// if any worker provides a tool whose bare name is "timeout", the
// reason worker treats it as this round's tool-call timeout. Provider-agnostic
// — the timer worker need not be named "timer". If no worker provides it, the
// timeout feature is simply disabled.
const timeoutTool = "timeout"

// timeoutToolFor returns the tool backing a name if it is the timeout tool.
func (w *BaseReasonWorker) timeoutToolFor(name string) (worker.Tool, bool) {
	t, ok := w.tools[name]
	if !ok {
		return worker.Tool{}, false
	}
	if t.Name == name && bareToolName(t) == timeoutTool {
		return t, true
	}
	return worker.Tool{}, false
}
