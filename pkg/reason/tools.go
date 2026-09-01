// The LLM-facing tool surface: which tools the model is offered, and sending
// the tool.request events for the calls it makes.
//
// Two halves meet here:
//
//   - the tool-list policy (ToolListBuilder / LLMToolDefs) decides what the
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
// the discovered capability universe: every own capability outside the
// provider.* management domain is exposed under its event-type name, and every
// peer capability under provider__name. With tool.request retired, there is no
// tool-vs-meta distinction at the event level — any capability is invoked by
// its own event, and the builder's policy decides what the LLM sees. Custom
// builders replace this.
func defaultToolListBuilder(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(caps))
	for _, cap := range caps {
		if cap.Source == w.ID() && strings.HasPrefix(string(cap.Event), "provider.") {
			// The provider.* management domain is not LLM-callable.
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
			Name:        toolNameFor(w, cap),
			Description: cap.Description,
			Parameters:  params,
		})
	}
	return defs
}

// LLMToolDefs builds the LLM tool list via the worker's tool-list builder
// over the discovered capability universe. Called at the start of every
// reasoning round.
func (w *BaseReasonWorker) LLMToolDefs() []llm.ToolDef {
	return w.toolListBuilder(w, w.discoveredCapabilities())
}

// toolNameFor is the LLM-facing name of a discovered capability: an own
// capability is its identifier (event type, or discriminator Key for a shared
// event) with dots → underscores; a peer capability is provider__identifier.
// It is the single naming used by both the tool-list builder and the reverse
// lookup (capabilityByToolName), so routing always agrees with the list.
func toolNameFor(w *BaseReasonWorker, cap DiscoveredCapability) string {
	id := string(cap.Event)
	if cap.KeyField != "" {
		id = cap.Key
	}
	if cap.Source == w.ID() {
		return strings.ReplaceAll(id, ".", "_")
	}
	return encodeToolName(w, worker.Tool{Name: id, Provider: cap.Source})
}

// capabilityByToolName finds a discovered capability by its LLM-facing tool
// name. Used to route LLM tool calls: the cap carries the owning Source and
// the event type to send. Own capabilities route back to self; peer
// capabilities go to their Source — both invoked by the cap's own event.
func (w *BaseReasonWorker) capabilityByToolName(name string) (DiscoveredCapability, bool) {
	for _, cap := range w.discoveredCapabilities() {
		if toolNameFor(w, cap) == name {
			return cap, true
		}
	}
	return DiscoveredCapability{}, false
}

// extensionToolName is the default LLM-facing name for an own extension: its
// identifier with dots → underscores (context.compress → context_compress).
// The identifier is the discriminator Key when the extension is multiplexed on
// a shared event, or the event type itself when the extension is identified by
// its own event. Used to reverse-map LLM tool calls back to registered
// extensions for meta (self-editing) detection; the routing itself reads the
// discovered universe via capabilityByToolName.
func extensionToolName(ext baseworker.Extension) string {
	id := ext.Key
	if ext.KeyField == "" {
		id = string(ext.Event)
	}
	return strings.ReplaceAll(id, ".", "_")
}

// ExtensionByToolName finds a registered own extension by its default
// LLM-facing tool name (extensionToolName). Used to detect self-editing (meta)
// extensions when routing an LLM call.
func (w *BaseReasonWorker) ExtensionByToolName(name string) (baseworker.Extension, bool) {
	for _, e := range w.Extensions() {
		if extensionToolName(e) == name {
			return e, true
		}
	}
	return baseworker.Extension{}, false
}

// sendToolRequests sends a directed event for each tool call to its target
// worker, using the capability's own event type and echoing the call's id as
// the RequestId. The tracker only manages the pending map; the caller is
// responsible for delivering the requests to the bus.
func (w *BaseReasonWorker) sendToolRequests(target, callerID string, calls []llm.ContentBlock, traceID string) {
	for _, tc := range calls {
		cap, ok := w.capabilityByToolName(tc.ToolName)
		if !ok {
			continue
		}
		var argsMap map[string]any
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		if cap.KeyField != "" {
			argsMap[cap.KeyField] = cap.Key
		}
		evt := event.New(cap.Event, callerID, map[string]any{
			"worker_id": callerID,
			"arguments": argsMap,
		})
		evt.RequestId = tc.ToolCallID
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
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
