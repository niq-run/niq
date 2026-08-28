// The default capability handlers: implementations of the capabilities every
// reason worker registers by default — the two generic tools (send_message /
// list_workers), the context meta ops (compress / rotate), the provider switch
// and the provider status queries. Kept separate from the actor-lifecycle
// dispatch (process.go) and the discovery machinery (tools.go).
package reason

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// registerCoreCapabilities registers the capabilities every reason worker has
// by default. Reason-family workers extend or replace these via Register.
func (w *BaseReasonWorker) registerCoreCapabilities() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(Capability{
		Event: event.TypeToolRequest, KeyField: "name", Key: "send_message",
		Description: "Send a message to a specific worker on the bus.",
		Parameters: obj(map[string]any{
			"target": map[string]any{"type": "string", "description": "Target worker ID"},
			"text":   map[string]any{"type": "string", "description": "Message text"},
		}),
	}, func(evt event.Event) {
		tc := worker.ParseToolCall(evt)
		w.handleSendMessage(tc.CallID, tc.Name, tc.CallerID, tc.Args)
	})

	w.Register(Capability{
		Event: event.TypeToolRequest, KeyField: "name", Key: "list_workers",
		Description: "List all available workers and their capabilities.",
		Parameters:  obj(map[string]any{}),
	}, func(evt event.Event) {
		tc := worker.ParseToolCall(evt)
		w.handleListWorkers(tc.CallID, tc.Name, tc.CallerID, tc.Args)
	})

	w.Register(Capability{
		Event: event.TypeWorkerUpdate, KeyField: "op", Key: "provider.switch",
		Description: "Switch active LLM provider/model.",
		Parameters: obj(map[string]any{
			"provider": map[string]any{"type": "string", "description": "Provider name"},
			"model":    map[string]any{"type": "string", "description": "Model name"},
		}),
	}, func(evt event.Event) {
		w.handleSetLLMProvider(evt)
	})

	w.Register(Capability{
		Event: event.TypeWorkerUpdate, KeyField: "op", Key: "context.compress",
		Description: "Compact your own context history: older messages are replaced by a summary, the most recent messages are kept.",
		Parameters: obj(map[string]any{
			"directive": map[string]any{"type": "string",
				"description": "Optional focus for the summary, e.g. what must be preserved."},
		}),
	}, func(evt event.Event) {
		w.handleContextOp(evt)
	})

	w.Register(Capability{
		Event: event.TypeWorkerUpdate, KeyField: "op", Key: "context.rotate",
		Description: "Rotate your context: summarize the current transcript as a carried digest and start a fresh context.",
		Parameters: obj(map[string]any{
			"carry": map[string]any{"type": "string",
				"description": "Optional instruction for what to carry into the digest."},
		}),
	}, func(evt event.Event) {
		w.handleContextOp(evt)
	})

	w.Register(Capability{
		Event: event.TypeWorkerQuery, KeyField: "subject", Key: "provider.list",
		Description: "List available providers and the current selection.",
	}, func(evt event.Event) {
		w.handleStatusQuery(evt)
	})

	w.Register(Capability{
		Event: event.TypeWorkerQuery, KeyField: "subject", Key: "provider.current",
		Description: "Current provider/model.",
	}, func(evt event.Event) {
		w.handleStatusQuery(evt)
	})
}

// handleSendMessage serves the send_message tool: it forwards the text to the
// target worker as a worker.input event and replies with a tool.completed.
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

// handleListWorkers serves the list_workers tool: it returns all known workers
// with their tools and published events, grouped by provider, and triggers a
// worker.discover to refresh the cache for the next call.
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

// handleContextOp executes a worker.update op=context.compress/rotate request:
// async transcript compaction/rotation. Registered as the core capability for
// both ops. The requester may carry a directive (compress focus) or a carry
// (rotate) in the payload; both are appended to the compaction directive.
func (w *BaseReasonWorker) handleContextOp(evt event.Event) {
	op, _ := evt.Payload["op"].(string)
	traceID := evt.TraceID
	isRotate := op == "context.rotate"

	directive := w.compactDirective()
	if extra, _ := evt.Payload["directive"].(string); !isRotate && extra != "" {
		directive = directive + "\nCaller focus: " + extra
	}
	if carry, _ := evt.Payload["carry"].(string); isRotate && carry != "" {
		directive = directive + "\nCarry into the new episode: " + carry
	}

	go func() {
		var err error
		if isRotate {
			if c, ok := w.compactor.(*DefaultCompactor); ok {
				err = c.Rotate(context.Background(), w.transcript, directive)
			} else {
				err = fmt.Errorf("rotate requested but compactor has no Rotate")
			}
		} else {
			err = w.compactor.Compact(context.Background(), w.transcript, directive)
		}
		// The transcript self-buffers Apply inputs during the edit and merges
		// them on commit, so no worker-side buffer is needed. Just note the
		// operation finished and schedule the next round (a brief lock for
		// the scheduling flags).
		w.mu.Lock()
		w.needReason = true
		w.mu.Unlock()

		log.Printf("[reason %s] meta op %s done: %v", w.ID(), op, err)
		done := event.New(event.TypeWorkerUpdated, w.ID(), map[string]any{
			"op": op, "done": true, "error": fmt.Sprintf("%v", err),
		})
		done.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), done)

		w.tryReason(context.Background())
	}()
}

// handleSetLLMProvider applies a worker.update op=provider.switch: it builds
// the named provider with an explicit model from the configured sources and
// atomically rebinds this worker's active provider under the lock. Both
// provider and model are required — an empty model is rejected rather than
// silently defaulting. The switch takes effect from the next reasoning round.
// Emits a worker.updated completion so the requester can observe success/failure.
func (w *BaseReasonWorker) handleSetLLMProvider(evt event.Event) {
	name, _ := evt.Payload["provider"].(string)
	model, _ := evt.Payload["model"].(string)

	payload := map[string]any{"op": "provider.switch", "provider": name, "model": model}

	var err error
	switch {
	case name == "":
		err = errors.New("provider is required")
	case model == "":
		err = errors.New("model is required (explicit model, no default fallback)")
	default:
		err = w.setActiveProvider(name, model)
	}

	if err != nil {
		payload["done"] = false
		payload["error"] = err.Error()
	} else {
		payload["done"] = true
		w.needReason = true
	}
	log.Printf("[reason %s] set provider=%s model=%s done=%t err=%v", w.ID(), name, model, payload["done"], err)
	w.emitWorkerUpdated(evt, payload)
}

// emitWorkerUpdated broadcasts a worker.updated completion for a meta op,
// carrying the caller's trace id.
func (w *BaseReasonWorker) emitWorkerUpdated(evt event.Event, payload map[string]any) {
	payload["op"] = payload["op"].(string)
	done := event.New(event.TypeWorkerUpdated, w.ID(), payload)
	done.TraceID = evt.TraceID
	_ = w.Channel.Broadcast(context.Background(), done)
}

// handleStatusQuery serves a worker.query request: subject=provider.list
// returns the configured providers (and their models) plus the worker's
// current provider/model; subject=provider.current returns just the current
// choice. Both reply with a worker.status snapshot carrying the same subject.
// Async — the requester observes the reply on the bus.
func (w *BaseReasonWorker) handleStatusQuery(evt event.Event) {
	subject, _ := evt.Payload["subject"].(string)

	payload := map[string]any{"subject": subject}
	switch subject {
	case "provider.list":
		payload["providers"] = w.availableProviders()
		payload["current"] = map[string]any{
			"provider": w.providerName,
			"model":    w.providerModel,
		}
	case "provider.current":
		payload["provider"] = w.providerName
		payload["model"] = w.providerModel
	default:
		payload["done"] = false
		payload["error"] = fmt.Sprintf("unknown worker.query subject: %q", subject)
	}

	done := event.New(event.TypeWorkerStatus, w.ID(), payload)
	done.TraceID = evt.TraceID
	_ = w.Channel.Broadcast(context.Background(), done)
	log.Printf("[reason %s] status_query subject=%s", w.ID(), subject)
}

// availableProviders returns the configured LLM providers and their models
// (nil when no ProviderSources — a single fixed provider). Served via the
// worker.query provider.* snapshots.
func (w *BaseReasonWorker) availableProviders() []ProviderInfo {
	if w.providerSources != nil {
		return w.providerSources.List()
	}
	return nil
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
