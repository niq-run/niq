// The LLM-facing tool surface: which tools the model is offered, and sending
// the invocation events for the calls it makes.
//
// Two halves meet here:
//
//   - the tool-list policy (ToolListBuilder / LLMToolDefs) decides what the
//     model sees, reading the bus-discovered universe from discovery.go, and
//   - sendToolRequests puts the model's chosen calls on the bus as
//     invocation events on each capability's own event type (tracked
//     afterwards by requesttracker.go).
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
// the discovered capability universe: every own capability is exposed under its
// event-type name (provider.* included — the worker may know and manage its own
// model supplier), and every peer capability under provider__name. Any
// capability is invoked by its own event type — the builder's policy decides
// what the LLM sees. Custom builders replace this.
func defaultToolListBuilder(w *BaseReasonWorker, caps []DiscoveredCapability) []llm.ToolDef {
	defs := make([]llm.ToolDef, 0, len(caps))
	for _, cap := range caps {
		defs = append(defs, llm.ToolDef{
			Name:        toolNameFor(w, cap),
			Description: cap.Description,
			Parameters:  cap.Parameters,
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
// capability is its event type with dots → underscores; a peer capability is
// provider__eventtype. It is the single naming used by both the tool-list
// builder and the reverse lookup (capabilityByToolName), so routing always
// agrees with the list.
func toolNameFor(w *BaseReasonWorker, cap DiscoveredCapability) string {
	id := string(cap.Event)
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
// event type with dots → underscores (context.compress → context_compress).
// Used to reverse-map LLM tool calls back to registered extensions for meta
// (self-editing) detection; the routing itself reads the discovered universe
// via capabilityByToolName.
func extensionToolName(ext baseworker.Extension) string {
	return strings.ReplaceAll(string(ext.Event), ".", "_")
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
		argsMap := map[string]any{}
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		evt := event.New(cap.Event, callerID, argsMap)
		evt.RequestId = tc.ToolCallID
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
}
