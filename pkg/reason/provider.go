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

// The provider management extensions use their own event types rather than
// multiplexing inside worker.update/worker.query — these are reason-worker
// specific, so they live here, not in core/event.
const (
	TypeProviderSwitch  event.EventType = "provider.switch"
	TypeProviderList    event.EventType = "provider.list"
	TypeProviderCurrent event.EventType = "provider.current"
)

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
		Event:       TypeProviderSwitch,
		Description: "Switch active LLM provider/model.",
		Parameters: obj(map[string]any{
			"provider": map[string]any{"type": "string", "description": "Provider name"},
			"model":    map[string]any{"type": "string", "description": "Model name"},
		}),
	}, func(evt event.Event) {
		w.handleSetLLMProvider(evt)
	})

	w.Register(baseworker.Extension{
		Event:       TypeProviderList,
		Description: "List available providers and the current selection.",
	}, func(evt event.Event) {
		w.handleStatusQuery(evt)
	})

	w.Register(baseworker.Extension{
		Event:       TypeProviderCurrent,
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

// handleSetLLMProvider applies a provider.switch request: it builds the named
// provider with an explicit model from the configured sources and atomically
// rebinds this worker's active provider under the lock. Both provider and
// model are required — an empty model is rejected rather than silently
// defaulting. The switch takes effect from the next reasoning round. Answers
// with request.completed / request.failed echoing the request's id.
func (w *BaseReasonWorker) handleSetLLMProvider(evt event.Event) {
	name, _ := evt.Payload["provider"].(string)
	model, _ := evt.Payload["model"].(string)

	payload := map[string]any{"provider": name, "model": model}
	var err error
	switch {
	case name == "":
		err = errors.New("provider is required")
	case model == "":
		err = errors.New("model is required (explicit model, no default fallback)")
	default:
		err = w.setActiveProvider(name, model)
	}
	log.Printf("[reason %s] set provider=%s model=%s err=%v", w.ID(), name, model, err)
	w.replyMeta(evt, payload, err)
}

// replyMeta answers a meta invocation with request.completed (nil err) or
// request.failed (err), echoing the request's id and trace. Broadcast like the
// worker.updated / worker.status completions it replaces, so bus subscribers
// (e.g. the webui) still observe them.
func (w *BaseReasonWorker) replyMeta(evt event.Event, payload map[string]any, err error) {
	typ := event.TypeRequestCompleted
	if err != nil {
		typ = event.TypeRequestFailed
		payload["error"] = err.Error()
	}
	done := event.New(typ, w.ID(), payload)
	done.RequestId = evt.RequestId
	done.TraceID = evt.TraceID
	_ = w.Channel.Broadcast(context.Background(), done)
}

// handleStatusQuery serves a provider.list / provider.current request: the
// former returns the configured providers (and their models) plus the current
// choice, the latter just the current choice. Both reply with
// request.completed echoing the request's id. Async — the requester observes
// the reply on the bus.
func (w *BaseReasonWorker) handleStatusQuery(evt event.Event) {
	payload := map[string]any{}
	switch evt.Type {
	case TypeProviderList:
		payload["providers"] = w.availableProviders()
		payload["current"] = map[string]any{
			"provider": w.providerName,
			"model":    w.providerModel,
		}
	case TypeProviderCurrent:
		payload["provider"] = w.providerName
		payload["model"] = w.providerModel
	default:
		w.replyMeta(evt, payload, fmt.Errorf("unknown provider query event: %q", evt.Type))
		return
	}
	w.replyMeta(evt, payload, nil)
	log.Printf("[reason %s] provider query %s", w.ID(), evt.Type)
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
