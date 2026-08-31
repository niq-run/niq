// The LLM-facing tool surface: which tools the model is offered, and sending
// the tool.request events for the calls it makes.
//
// Two halves meet here:
//
//   - the tool-list policy (ToolListBuilder / llmToolDefs) decides what the
//     model sees, reading the bus-discovered universe from discovery.go, and
//   - sendToolRequests puts the model's chosen calls on the bus as
//     tool.request events (tracked afterwards by requesttracker.go).
//
// This file does not own any discovery state; it reads it.
package reason

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/baseworker"
)

// ToolListBuilder produces the LLM tool list from the discovered capability
// universe. Extensions carry no exposure marks — the builder decides, by its
// own policy, which capabilities the LLM sees and under what names. This is an
// extension point: a reason-family worker replaces it to control exactly what
// its LLM can call.
type ToolListBuilder func(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef

// defaultToolListBuilder is the default policy producing the LLM tool list from
// the discovered capability universe: own tool.request capabilities and own
// meta capabilities outside the provider.* management domain are exposed (meta
// capabilities under their extensionToolName); peer tool.request capabilities are
// exposed under provider__name. Custom builders replace this.
func defaultToolListBuilder(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(caps))
	for _, cap := range caps {
		var name string
		switch {
		case cap.Source == w.ID() && cap.Event == event.TypeToolRequest:
			// Own tool (tool.request + name): expose under its bare tool name.
			name = cap.Key
		case cap.Source == w.ID() && !strings.HasPrefix(string(cap.Event), "provider."):
			// Own meta capability: expose under its event-type name
			// (context.compress → context_compress). The provider.* management
			// domain is excluded — it is not LLM-callable.
			name = extensionToolName(baseworker.Extension{Event: cap.Event, KeyField: cap.KeyField, Key: cap.Key})
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

// extensionToolName is the default LLM-facing name for an own extension: its
// identifier with dots → underscores (context.compress → context_compress).
// The identifier is the discriminator Key when the extension is multiplexed on
// a shared event (tool.request + name), or the event type itself when the
// extension is identified by its own event (context.compress). The default
// tool-list builder names exposed extensions with it, and the reason worker
// reverse-maps LLM tool calls back to meta extensions with the same
// convention, so a custom builder that exposes a meta extension must keep this
// name for the meta routing to recognize it.
func extensionToolName(ext baseworker.Extension) string {
	id := ext.Key
	if ext.KeyField == "" {
		id = string(ext.Event)
	}
	return strings.ReplaceAll(id, ".", "_")
}

// extensionByToolName finds a registered extension by its default LLM-facing
// tool name (extensionToolName). Used to route LLM tool calls: a meta
// extension is triggered by converting the tool call back into its
// worker.update/worker.query event; an own tool extension loops back as
// tool.request.
func (w *BaseReasonWorker) extensionByToolName(name string) (baseworker.Extension, bool) {
	for _, e := range w.Extensions() {
		if extensionToolName(e) == name {
			return e, true
		}
	}
	return baseworker.Extension{}, false
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
			"name":      tc.ToolName,
			"arguments": argsMap,
		})
		evt.RequestId = tc.ToolCallID
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
}

// replyUnknownTool replies to a tool call no registered extension handled.
func (w *BaseReasonWorker) replyUnknownTool(tc baseworker.ToolCall) {
	w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "Unknown tool: "+tc.Name, tc.TraceID)
}

// cloneParams returns a shallow copy of a parameter map, so a caller can add
// or delete a key (e.g. folding in / stripping out a discriminator) without
// mutating the original schema it clones.
func cloneParams(p map[string]any) map[string]any {
	out := make(map[string]any, len(p))
	for k, v := range p {
		out[k] = v
	}
	return out
}
