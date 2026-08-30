// The base capability handlers: the provider management capabilities every
// reason worker registers (provider.switch / provider.list / provider.current).
// The generic tools (send_message / list_workers) and the context meta ops
// (compress / rotate) belong to the default reason worker implementation
// (pkg/worker/reason), not to the shared mechanism.
package reason

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/event"
)

// registerBaseCapabilities registers the provider management capabilities every
// reason worker has. Reason-family workers extend or replace these via Register;
// the default worker adds its own toolkit on top (send_message, list_workers,
// context.compress/rotate) in pkg/worker/reason.
func (w *BaseReasonWorker) registerBaseCapabilities() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

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
		// No needReason here: the rebinding above already applies to every
		// later round, and scheduling one would make the worker answer
		// immediately with the new model — a side effect of what is meant to be
		// a configuration change. The new provider/model is picked up by the
		// next round the conversation itself asks for.
		payload["done"] = true
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
