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

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/core/worker"
)

func (w *BaseReasonWorker) allTools() []worker.Tool {
	tools := make([]worker.Tool, 0, len(w.tools))
	for _, t := range w.tools {
		tools = append(tools, t)
	}
	return tools
}

// watchDeclarations renders this worker's capability contract as the
// worker.ready "watch" payload: the events this worker responds to, derived
// from the capability registry. tool.request entries carry the tool name
// top-level; worker.update / worker.query entries fold the discriminator (op /
// subject) into parameters. The declaration is consumer-agnostic — whether a
// capability is exposed to an LLM is a policy concern, not this declaration's.
func (w *BaseReasonWorker) watchDeclarations() []map[string]any {
	out := make([]map[string]any, 0, len(w.capabilities))
	for _, rc := range w.capabilities {
		cap := rc.cap
		entry := map[string]any{
			"event": string(cap.Event),
			"desc":  cap.Description,
		}
		if cap.Event == event.TypeToolRequest {
			entry["name"] = cap.Key
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
		out = append(out, entry)
	}
	return out
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
			name = cap.Source + "__" + cap.Key
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

// handleWorkerReady learns a worker's capabilities and published events from
// its worker.ready announcement, feeding the unified discovery universe
// (discovered) and the tool table used for dispatch. The worker's own
// self-directed announcement is processed the same way as any peer's (it
// refreshes the own capabilities the same way).
func (w *BaseReasonWorker) handleWorkerReady(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)
	if workerID == "" {
		return
	}

	// Re-announcement is idempotent: drop this worker's previous view.
	w.discovered = removeDiscoveredCaps(w.discovered, workerID)
	for name, tool := range w.tools {
		if tool.Provider == workerID {
			delete(w.tools, name)
			delete(w.toolNameMap, name)
		}
	}

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

	// Parse tools (legacy announcements) into both the discovery universe and
	// the tool table used for dispatch.
	b, err := json.Marshal(evt.Payload["tools"])
	if err == nil {
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
				t := worker.Tool{
					Name:        name,
					Description: desc,
					Parameters:  params,
					Provider:    workerID,
				}
				t.Name = encodeToolName(w, t)
				// Save the reverse mapping (encoded name -> original declared
				// name) so dispatch can hand a worker back its exact dotted
				// tool name, e.g. provider__program.search -> program.search.
				w.toolNameMap[t.Name] = name
				w.tools[t.Name] = t
			}
			if len(toolsRaw) > 0 {
				log.Printf("[reason %s] received %d tool(s) from %s", w.ID(), len(toolsRaw), workerID)
			}
		}
	}

	// Parse publishes.
	b, err = json.Marshal(evt.Payload["publishes"])
	if err == nil {
		var eventsRaw []EventPublish
		if err := json.Unmarshal(b, &eventsRaw); err == nil && len(eventsRaw) > 0 {
			w.publishMap[workerID] = eventsRaw
			log.Printf("[reason %s] received %d event(s) from %s", w.ID(), len(eventsRaw), workerID)
		}
	}
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
	for name, tool := range w.tools {
		if tool.Provider == workerID {
			delete(w.tools, name)
			delete(w.toolNameMap, name)
		}
	}
	delete(w.publishMap, workerID)
	log.Printf("[reason %s] removed tools and events from %s", w.ID(), workerID)
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
