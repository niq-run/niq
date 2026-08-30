// worker presence tracking and tool aggregation.
//
// handleWorkerReady / handleWorkerGone: learn/forget a worker's tools & events
// allTools: return all known tools (discovered)
// toolDefs: build LLM tool definitions from the encoded tool names
// encodeToolName + toolNameMap: encode a declared tool name for the LLM (dots
//
//	-> underscores, provider__ prefix) and remember the original declared name
//	so dispatch can restore it (e.g. provider__program.search -> program.search).
//
// publishToolRequests: send tool.request to target workers
package reason

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/worker"
)

func (w *BaseReasonWorker) allTools() []worker.Tool {
	tools := make([]worker.Tool, 0, len(w.tools))
	for _, t := range w.tools {
		tools = append(tools, t)
	}
	return tools
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

// DiscoveredWorkers aggregates this worker's known tools and published events by
// provider — the payload behind list_workers. It is a read-only view of the
// mechanism's bus-discovered state (w.tools / w.publishMap), not a worker
// strategy and not this worker's own state Snapshot: the list_workers
// capability (pkg/worker/reason) calls it and sends the result. Kept here, next
// to the discovery data it reads.
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

// watchEntries renders every registered capability into the worker.ready
// "watch" wire format — the full contract, SelfOnly included. It is the
// self-directed announcement, from which the worker learns its complete own
// view. The peer-facing broadcast is built by broadcastReady, which filters
// SelfOnly capabilities out before rendering; the renderer itself carries no
// policy.
func (w *BaseReasonWorker) watchEntries() []map[string]any {
	out := make([]map[string]any, 0, len(w.capabilities))
	for _, rc := range w.capabilities {
		out = append(out, w.watchEntry(rc.cap))
	}
	return out
}

// watchEntry renders one capability into a worker.ready "watch" entry.
// tool.request entries carry the tool name top-level; worker.update /
// worker.query entries fold the discriminator (op / subject) into parameters.
func (w *BaseReasonWorker) watchEntry(cap Capability) map[string]any {
	entry := map[string]any{
		"event": string(cap.Event),
		"desc":  cap.Description,
	}
	if cap.Event == event.TypeToolRequest {
		entry["name"] = cap.Key
		// Carry the schema too, so a peer tool's parameters reach both the
		// LLM tool list and the dispatch table.
		if len(cap.Parameters) > 0 {
			entry["parameters"] = cap.Parameters
		}
	} else if cap.KeyField != "" {
		params := cap.Parameters
		if params == nil {
			params = map[string]any{}
		}
		params = cloneParams(params)
		params[cap.KeyField] = cap.Key
		entry["parameters"] = params
	} else if len(cap.Parameters) > 0 {
		entry["parameters"] = cap.Parameters
	}
	return entry
}

func cloneParams(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}

// ToolListBuilder produces the LLM tool list from the discovered capability
// universe. Capabilities carry no exposure marks — the builder decides, by its
// own policy, which capabilities the LLM sees and under what names. This is an
// extension point: a reason-family worker replaces it to control exactly what
// its LLM can call.
type ToolListBuilder func(w *BaseReasonWorker, caps []DiscoveredCap) []llm.ToolDef

// defaultToolListBuilder is the default policy producing the LLM tool list from
// the discovered capability universe: own tool.request capabilities and own
// meta capabilities outside the provider.* management domain are exposed (meta
// capabilities under their capToolName); peer tool.request capabilities are
// exposed under provider__name. Custom builders replace this.
func defaultToolListBuilder(w *BaseReasonWorker, caps []DiscoveredCap) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(caps))
	for _, cap := range caps {
		var name string
		switch {
		case cap.Source == w.ID() && cap.Event == event.TypeToolRequest:
			name = cap.Key
		case cap.Source == w.ID() && !strings.HasPrefix(cap.Key, "provider."):
			name = capToolName(Capability{Key: cap.Key})
		case cap.Source != w.ID() && cap.Event == event.TypeToolRequest:
			// Same encoding as the dispatch table, so the name the LLM sees is
			// exactly the name dispatch looks up (dots -> underscores, and the
			// source prefix).
			name = encodeToolName(w, worker.Tool{Name: cap.Key, Provider: cap.Source})
		default:
			continue
		}
		params := cap.Parameters
		// Watch-derived entries fold the discriminator (op/subject) into the
		// parameters; it must not leak into the LLM tool schema.
		if cap.KeyField != "" {
			params = cloneParams(params)
			delete(params, cap.KeyField)
		}
		defs = append(defs, llm.ToolDef{
			Name:        name,
			Description: cap.Description,
			Parameters:  params,
		})
	}
	return defs
}

// llmToolDefs builds the LLM tool list via the worker's tool-list builder
// over the discovered capability universe. Called at the start of every
// reasoning round.
func (w *BaseReasonWorker) llmToolDefs() []llm.ToolDef {
	return w.toolListBuilder(w, w.discoveredCapabilities())
}

// DiscoveredCap is a capability as seen on the bus: which worker declared it
// and the event it responds to. It is the unified unit the tool-list builder
// operates on — this worker's own contract (learned from its self-directed
// worker.ready) and every peer's presence broadcast are presented uniformly,
// distinguished only by Source. This is the bus-discovered universe, NOT the
// worker's own declared registry: BaseReasonWorker learns it by observing
// worker.ready / worker.gone, so it describes what the worker can SEE, not what
// it announces. The self-registry (Capability / Register) lives in
// capability.go; this type belongs with the discovery machinery below.
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
// by every worker.ready announcement — the worker's own directed full contract
// and every peer's presence broadcast — so the builder sees one bus-derived
// view, no two-source assembly.
func (w *BaseReasonWorker) discoveredCapabilities() []DiscoveredCap {
	return w.discovered
}

// handleWorkerReady learns a worker's capabilities and published events from
// its worker.ready announcement, feeding the unified discovery universe
// (discovered) and the tool table used for dispatch. Each ready event carries
// that worker's complete contract (the worker excludes itself from its own
// presence broadcast and sends its full contract to itself), so the source's
// previous view is replaced wholesale — which is also what lets a
// re-announcement update a worker's capabilities.
func (w *BaseReasonWorker) handleWorkerReady(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)
	if workerID == "" {
		return
	}

	// Re-announcement is idempotent: drop this worker's previous view.
	w.discovered = removeDiscoveredCaps(w.discovered, workerID)

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

	// Parse tools (legacy announcements) into the discovery universe.
	if b, err := json.Marshal(evt.Payload["tools"]); err == nil {
		var toolsRaw []map[string]any
		if err := json.Unmarshal(b, &toolsRaw); err == nil {
			for _, m := range toolsRaw {
				name, _ := m["name"].(string)
				desc, _ := m["description"].(string)
				params, _ := m["parameters"].(map[string]any)
				if name == "" {
					continue
				}
				w.discovered = append(w.discovered, DiscoveredCap{
					Source: workerID, Event: event.TypeToolRequest, KeyField: "name",
					Key: name, Description: desc, Parameters: params,
				})
			}
			if len(toolsRaw) > 0 {
				log.Printf("[reason %s] received %d tool(s) from %s", w.ID(), len(toolsRaw), workerID)
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

// discoveredFromWatch parses a worker.ready "watch" entry into a DiscoveredCap.
// tool.request entries carry the tool name top-level; worker.update / worker.query
// entries fold the discriminator (op / subject) into parameters.
func discoveredFromWatch(source string, e map[string]any) (DiscoveredCap, bool) {
	typ, _ := e["event"].(string)
	if typ == "" {
		return DiscoveredCap{}, false
	}
	dc := DiscoveredCap{
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

// removeDiscoveredCaps returns caps without the entries declared by source.
func removeDiscoveredCaps(caps []DiscoveredCap, source string) []DiscoveredCap {
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

	w.discovered = removeDiscoveredCaps(w.discovered, workerID)
	delete(w.publishMap, workerID)
	// The dispatch table is a derived index; drop the departed worker's tools
	// the same way as everything else — by rebuilding from discovered.
	w.rebuildDispatchTable()
	log.Printf("[reason %s] removed tools and events from %s", w.ID(), workerID)
}

// rebuildDispatchTable derives the dispatch table (w.tools + toolNameMap) from
// the discovery universe, so the two stay in step and the discovery universe is
// the single source of truth. Only peer tool.request capabilities become
// callable tools: own capabilities dispatch through the registry instead, so
// they are intentionally not indexed here. Called whenever discovered changes
// (handleWorkerReady / handleWorkerGone).
func (w *BaseReasonWorker) rebuildDispatchTable() {
	for name := range w.tools {
		delete(w.tools, name)
		delete(w.toolNameMap, name)
	}
	for _, cap := range w.discovered {
		if cap.Event != event.TypeToolRequest || cap.Source == w.ID() || cap.Key == "" {
			continue
		}
		t := worker.Tool{Name: cap.Key, Description: cap.Description,
			Parameters: cap.Parameters, Provider: cap.Source}
		t.Name = encodeToolName(w, t)
		// Reverse mapping (encoded name -> original declared name) so dispatch
		// can hand a worker back its exact dotted name, e.g.
		// provider__program.search -> program.search.
		w.toolNameMap[t.Name] = cap.Key
		w.tools[t.Name] = t
	}
}

// setToolTimeoutTool is the bare-name contract for the tool built-in timeout:
// if any worker provides a tool whose bare name is "set_tool_timeout", the
// reason worker treats it as this round's tool-call timeout. Provider-agnostic
// — the timer worker need not be named "timer". If no worker provides it, the
// timeout feature is simply disabled.
const setToolTimeoutTool = "set_tool_timeout"

// timeoutToolFor returns the tool backing a name if it is the timeout tool.
func (w *BaseReasonWorker) timeoutToolFor(name string) (worker.Tool, bool) {
	t, ok := w.tools[name]
	if !ok {
		return worker.Tool{}, false
	}
	if t.Name == name && bareToolName(t) == setToolTimeoutTool {
		return t, true
	}
	return worker.Tool{}, false
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

// replyUnknownTool replies to a tool call no capability handled.
func (w *BaseReasonWorker) replyUnknownTool(tc worker.ToolCall) {
	w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "Unknown tool: "+tc.Name, tc.TraceID)
}

// toolDefs builds the LLM tool definitions from the known tools. Tools are
// stored in w.tools with their LLM-facing name already encoded (encodeToolName
// in handleWorkerReady), so the definition reuses t.Name as-is — re-encoding
// here would double the provider prefix.
func toolDefs(w *BaseReasonWorker, tools []worker.Tool) []llm.ToolDef {
	out := make([]llm.ToolDef, len(tools))
	for i, t := range tools {
		out[i] = llm.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		}
	}
	return out
}

// encodeToolName maps a Tool to the name both the w.tools table and the LLM
// use. A worker's own tools (provider empty or this worker) keep a bare name
// (with inner '.' -> '_'); tools backed by another worker become provider__name, so
// the worker/tool boundary is unambiguous. Invariant: worker IDs and tool
// names must not contain "__" (double underscore); '__' is the separator.
//
// The original declared name is preserved in w.toolNameMap (see
// handleWorkerReady); dispatch restores it so a worker receives its exact
// dotted tool name rather than the encoded form.
func encodeToolName(w *BaseReasonWorker, t worker.Tool) string {
	p, n := t.Provider, t.Name
	if p == "" || p == w.ID() {
		return strings.ReplaceAll(n, ".", "_")
	}
	return p + "__" + strings.ReplaceAll(n, ".", "_")
}

// sendToolRequests sends a directed tool.request event for each tool call
// to its target worker. The tracker only manages the pending map; the caller
// is responsible for delivering the requests to the bus.
func (w *BaseReasonWorker) sendToolRequests(target, callerID string, calls []llm.ContentBlock, traceID string) {
	for _, tc := range calls {
		var argsMap map[string]any
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		evt := event.New(event.TypeToolRequest, callerID, map[string]any{
			"worker_id": callerID,
			"call_id":   tc.ToolCallID,
			"name":      tc.ToolName,
			"arguments": argsMap,
		})
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
}
