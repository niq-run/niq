// Provider management: the selectable LLM providers a reason worker can
// switch between, exposed to the bus through the extension mechanism.
//
// Two halves meet here:
//   - the sources model (ProviderSources / ProviderInfo) and the selection
//     logic (setActiveProvider / resolveContextWindow), and
//   - the extensions that make providers controllable over the bus:
//     provider.switch (worker.update) and provider.list / provider.current
//     (worker.query).
//
// The generic tools (send_message / list_workers) and the context meta ops
// (compress / rotate) are NOT here — they belong to the default reason worker
// implementation (pkg/worker/reason).
package reason

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/event"
	llm "github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/baseworker"
)

// ProviderInfo describes one selectable provider a reason worker can switch to
// at runtime: its name, the default model, and the full model list. Models is
// the list of model IDs (for display); ModelDetails carries the same models
// with metadata such as ContextWindow when known.
// The json tags are the wire format: these travel in the worker.status
// snapshot (worker.query provider.list) and are read by the WebUI, so they use
// the same snake_case naming as llm.ModelInfo rather than Go field names.
type ProviderInfo struct {
	Name         string          `json:"name"`
	Default      string          `json:"default"`
	Models       []string        `json:"models"`
	ModelDetails []llm.ModelInfo `json:"model_details,omitempty"`
}

// ProviderSources is the runtime selection point for a reason worker's LLM
// provider. A worker can be given many providers (instead of a single baked-in
// one) and switch the active provider/model later via worker.update
// without depending on any concrete provider or config package.
type ProviderSources interface {
	// Default returns the initially active provider. It may be nil when no
	// provider is configured.
	Default() llm.LLMProvider

	// Build constructs a provider bound to name and an explicit model. An
	// empty model selects the provider's default model. An unknown provider
	// name or an invalid model is an error.
	Build(name, model string) (llm.LLMProvider, error)

	// List enumerates the available providers and their models.
	List() []ProviderInfo
}

// registerBaseExtensions registers the provider management extensions every
// reason worker has (provider.switch / provider.list / provider.current).
// Reason-family workers extend or replace these via Register; the default
// worker adds its own toolkit on top (send_message, list_workers,
// context.compress/rotate) in pkg/worker/reason.
func (w *BaseReasonWorker) registerBaseExtensions() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event: event.TypeWorkerUpdate, KeyField: "op", Key: "provider.switch",
		Description: "Switch active LLM provider/model.",
		Parameters: obj(map[string]any{
			"provider": map[string]any{"type": "string", "description": "Provider name"},
			"model":    map[string]any{"type": "string", "description": "Model name"},
		}),
	}, func(evt event.Event) {
		w.handleSetLLMProvider(evt)
	})

	w.Register(baseworker.Extension{
		Event: event.TypeWorkerQuery, KeyField: "subject", Key: "provider.list",
		Description: "List available providers and the current selection.",
	}, func(evt event.Event) {
		w.handleStatusQuery(evt)
	})

	w.Register(baseworker.Extension{
		Event: event.TypeWorkerQuery, KeyField: "subject", Key: "provider.current",
		Description: "Current provider/model.",
	}, func(evt event.Event) {
		w.handleStatusQuery(evt)
	})
}

// setActiveProvider switches this worker's active LLM provider. It must be
// called with w.mu held. The provider is built from the configured sources.
// The default LLM-summary compactor reads the active provider lazily (via
// LLMProvider on every call), so no rebinding is needed here when the provider
// is switched.
func (w *BaseReasonWorker) setActiveProvider(name, model string) error {
	if w.providerSources == nil {
		return errors.New("no provider sources configured on this worker")
	}
	prov, err := w.providerSources.Build(name, model)
	if err != nil {
		return err
	}
	w.llmProvider = prov
	w.providerName = name
	w.providerModel = model
	w.resolveContextWindow()
	return nil
}

// resolveContextWindow sets w.contextWindow to the effective context-window size
// for the currently selected provider/model. An explicit params.context_window
// (already loaded into w.contextWindow) always wins; otherwise the window is
// taken from the selected model's reported metadata. Called at init and on
// every provider switch. Expects w.mu held.
//
// Only the exact selected model is consulted. Models on the same provider can
// have very different windows, so borrowing another model's would silently
// apply a wrong budget. If the selected model reports no window,
// w.contextWindow stays 0 and budget handling remains disabled.
func (w *BaseReasonWorker) resolveContextWindow() {
	if w.contextWindow > 0 {
		return // explicit override wins
	}
	if w.providerSources == nil {
		return
	}
	for _, info := range w.providerSources.List() {
		if info.Name != w.providerName {
			continue
		}
		model := w.providerModel
		if model == "" {
			model = info.Default
		}
		for _, m := range info.ModelDetails {
			if m.ID == model && m.ContextWindow > 0 {
				w.contextWindow = m.ContextWindow
				return
			}
		}
	}
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
