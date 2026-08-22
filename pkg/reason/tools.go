// worker presence tracking and tool aggregation.
//
// handleWorkerReady / handleWorkerGone: learn/forget a worker's tools & events
// allTools: return all known tools (discovered + own)
// handleToolRequest: process tool.requested for this worker's tools
// toolDefs / sanitize: build LLM tool definitions (dot → underscore names)
// publishToolRequests: send tool.requested to target workers
package reason

import (
	"context"
	"encoding/json"
	"fmt"
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

// ToolProvider supplies the tools this worker exposes on the bus — the
// LLM-facing definitions it can invoke and how calls are served. This same
// worker both calls and answers them (a call is routed back to it). The
// embedding worker composes its own; nil uses the default provider. Tools
// backed by other workers are discovered separately.
type ToolProvider interface {
	// ToolDefinitions returns the tools this provider can handle, as the table
	// the LLM sees (send_message, list_workers, context.compress, ...).
	ToolDefinitions() []worker.Tool
	// HandleToolCall serves one tool.requested targeting this worker.
	HandleToolCall(tc worker.ToolCall)
}

// DefaultTools is the default ToolProvider: the four domain-agnostic
// capabilities any reason worker exposes on the bus. They are declared with
// this worker as provider, so only this worker can call them, but they are
// ordinary bus-declared abilities, not a special built-in set.
type DefaultTools struct {
	w *BaseReasonWorker
}

// metaOpOf maps a meta tool's bare name to its worker.update op: the tool is
// the LLM-facing surface (context.compress), the op is the event payload's
// operation name (compress).
func metaOpOf(toolName string) string {
	switch toolName {
	case "context_compress":
		return "compress"
	case "context_rotate":
		return "rotate"
	default:
		return toolName
	}
}

// defaultToolDefinitions are the schemas of the four default tools.
var defaultToolDefinitions = []worker.Tool{
	{
		Name:        "send_message",
		Description: "Send a message to a specific worker on the bus.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "Target worker ID"},
				"text":   map[string]any{"type": "string", "description": "Message text"},
			},
			"required": []any{"target", "text"},
		},
	},
	{
		Name:        "list_workers",
		Description: "List all available workers and their capabilities. Returns tools and events published by each worker. Call this first, then set a 2-second timer, then call again to get the latest worker information after re-discovery.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	},
	{
		Name:        "context.compress",
		Description: "Compact your own context history: older messages are replaced by a summary, the most recent messages are kept. Call this when the system reminds you about context budget, or when earlier history is no longer needed in full. This operation edits your own context; issue it alone (other tool calls in the same turn will be rejected while it runs).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"directive": map[string]any{"type": "string",
					"description": "Optional focus for the summary, e.g. what must be preserved."},
			},
		},
		IsMetaTool: true,
	},
	{
		Name:        "context.rotate",
		Description: "Rotate your context: summarize the current transcript as a carried digest and start a fresh context containing only that digest. Use for periodic/discrete tasks when previous rounds are no longer relevant, or when starting a new topic. This operation edits your own context; issue it alone (other tool calls in the same turn will be rejected while it runs).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"carry": map[string]any{"type": "string",
					"description": "Optional instruction for what to carry into the digest (conclusions, open items, references)."},
			},
		},
		IsMetaTool: true,
	},
}

// selfToolDeclarations renders this worker's own tool + meta-tool declarations
// as the worker.ready "tools" payload a reason worker publishes to itself. It
// delegates to the tool provider, carrying each tool's schema and its IsMetaTool
// flag so a discovering worker can route meta tools to worker.update instead of
// tool.requested.
func (w *BaseReasonWorker) selfToolDeclarations() []map[string]any {
	defs := w.toolProvider.ToolDefinitions()
	out := make([]map[string]any, 0, len(defs))
	for _, td := range defs {
		out = append(out, map[string]any{
			"name":         td.Name,
			"description":  td.Description,
			"parameters":   td.Parameters,
			"is_meta_tool": td.IsMetaTool,
		})
	}
	return out
}

// NewDefaultTools builds the default provider bound to a worker.
func NewDefaultTools(w *BaseReasonWorker) *DefaultTools {
	return &DefaultTools{w: w}
}

// ToolDefinitions implements ToolProvider for the four default tools, tagged
// with this worker's ID so they route back to it.
func (t *DefaultTools) ToolDefinitions() []worker.Tool {
	out := make([]worker.Tool, len(defaultToolDefinitions))
	for i, td := range defaultToolDefinitions {
		td.Provider = t.w.ID()
		out[i] = td
	}
	return out
}

// HandleToolCall implements ToolProvider, dispatching the four defaults.
// compress/rotate are meta operations: they edit this worker's own state and
// are reached via worker.update (from the LLM's tool call converted by
// handleToolCalls, or from external requesters), not via tool.requested.
func (t *DefaultTools) HandleToolCall(tc worker.ToolCall) {
	switch tc.Name {
	case "send_message":
		t.w.handleSendMessage(tc.CallID, tc.Name, tc.CallerID, tc.Args)
	case "list_workers":
		t.w.handleListWorkers(tc.CallID, tc.Name, tc.CallerID, tc.Args)
	case "context.compress", "context.rotate":
		t.w.ReplyRejected(tc.CallerID, tc.CallID, tc.Name,
			"meta operation: send a worker.update event to this worker instead of calling this as a tool", tc.TraceID)
	default:
		t.w.replyUnknownTool(tc)
	}
}

// handleWorkerReady learns a worker's tools and published events from its
// worker.ready announcement, updating the known tool set and publishes map.
func (w *BaseReasonWorker) handleWorkerReady(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)

	// Parse tools.
	b, err := json.Marshal(evt.Payload["tools"])
	if err == nil {
		var toolsRaw []map[string]any
		if err := json.Unmarshal(b, &toolsRaw); err == nil {
			for _, m := range toolsRaw {
				name, _ := m["name"].(string)
				desc, _ := m["description"].(string)
				params, _ := m["parameters"].(map[string]any)
				isMeta, _ := m["is_meta_tool"].(bool)
				if name == "" {
					continue
				}
				t := worker.Tool{
					Name:        name,
					Description: desc,
					Parameters:  params,
					Provider:    workerID,
					IsMetaTool:  isMeta,
				}
				t.Name = encodeToolName(w, t)
				w.tools[t.Name] = t
			}
			log.Printf("[reason %s] received %d tool(s) from %s", w.ID(), len(toolsRaw), workerID)
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

// handleWorkerGone forgets a departed worker's tools and published events.
func (w *BaseReasonWorker) handleWorkerGone(evt event.Event) {
	workerID, _ := evt.Payload["worker_id"].(string)

	for name, tool := range w.tools {
		if tool.Provider == workerID {
			delete(w.tools, name)
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

// handleToolRequest processes a tool.requested event targeting this worker by
// dispatching to its tool provider.
func (w *BaseReasonWorker) handleToolRequest(evt event.Event) {
	tc := worker.ParseToolCall(evt)
	w.toolProvider.HandleToolCall(tc)
}

// replyUnknownTool replies to a tool call no provider handled.
func (w *BaseReasonWorker) replyUnknownTool(tc worker.ToolCall) {
	w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "Unknown tool: "+tc.Name, tc.TraceID)
}

func (w *BaseReasonWorker) handleSendMessage(callID, toolName, callerID string, args map[string]any) {
	target, _ := args["target"].(string)
	text, _ := args["text"].(string)
	if target == "" || text == "" {
		evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
			"call_id": callID, "name": toolName,
			"error": "target and text are required",
		})
		_ = w.Channel.Send(context.Background(), evt, callerID)
		return
	}

	msgEvt := event.New(event.TypeWorkerInput, w.ID(), map[string]any{
		"text": text,
	})
	msgEvt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), msgEvt, target)

	w.sendSuccess(callID, toolName, callerID,
		fmt.Sprintf("message sent to %s", target))
}

// handleListWorkers returns all known workers with their tools and published
// events, grouped by provider. It also triggers a worker.discover to refresh
// the cache for the next call.
func (w *BaseReasonWorker) handleListWorkers(callID, toolName, callerID string, args map[string]any) {
	// Trigger re-discovery so the next call gets fresh data.
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), nil))

	// Aggregate tools and publishes by provider.
	type workerInfo struct {
		WorkerID  string         `json:"worker_id"`
		Tools     []worker.Tool  `json:"tools,omitempty"`
		Publishes []EventPublish `json:"publishes,omitempty"`
	}

	providers := make(map[string]*workerInfo)

	// Collect tools grouped by provider.
	for _, tool := range w.tools {
		info, ok := providers[tool.Provider]
		if !ok {
			info = &workerInfo{
				WorkerID:  tool.Provider,
				Publishes: w.publishMap[tool.Provider],
			}
			providers[tool.Provider] = info
		}
		info.Tools = append(info.Tools, tool)
	}

	// Collect providers that only publish events (no tools).
	for provider, events := range w.publishMap {
		if _, ok := providers[provider]; !ok {
			providers[provider] = &workerInfo{
				WorkerID:  provider,
				Publishes: events,
			}
		}
	}

	result := make([]workerInfo, 0, len(providers))
	for _, info := range providers {
		result = append(result, *info)
	}

	b, err := json.Marshal(result)
	if err != nil {
		w.sendFail(callID, toolName, callerID, fmt.Sprintf(
			"list_workers could not serialize the worker list: a worker's announced tool/event carried "+
				"a field that cannot be serialized (%v). This usually means a worker.ready declared an invalid "+
				"schema. Ask that worker to fix its declaration, then retry.", err))
		return
	}

	w.sendSuccess(callID, toolName, callerID, string(b))
	log.Printf("[reason %s] list_workers → %d workers", w.ID(), len(result))
}

func (w *BaseReasonWorker) sendSuccess(callID, toolName, callerID, result string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID, "name": toolName,
		"result": result,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

func (w *BaseReasonWorker) sendFail(callID, toolName, callerID, errMsg string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID, "name": toolName,
		"error": errMsg,
	})
	evt.TraceID = w.currentTraceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
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
func encodeToolName(w *BaseReasonWorker, t worker.Tool) string {
	p, n := t.Provider, t.Name
	if p == "" || p == w.ID() {
		return strings.ReplaceAll(n, ".", "_")
	}
	return p + "__" + strings.ReplaceAll(n, ".", "_")
}

// sendToolRequests sends a directed tool.requested event for each tool call
// to its target worker. The tracker only manages the pending map; the caller
// is responsible for delivering the requests to the bus.
func (w *BaseReasonWorker) sendToolRequests(target, callerID string, calls []llm.ContentBlock, traceID string) {
	for _, tc := range calls {
		var argsMap map[string]any
		if tc.ToolArguments != "" {
			json.Unmarshal([]byte(tc.ToolArguments), &argsMap)
		}
		evt := event.New(event.TypeToolRequested, callerID, map[string]any{
			"worker_id": callerID,
			"call_id":   tc.ToolCallID,
			"name":      tc.ToolName,
			"arguments": argsMap,
		})
		evt.TraceID = traceID
		_ = w.Channel.Send(context.Background(), evt, target)
	}
}
