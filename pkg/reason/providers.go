package reason

import (
	"errors"

	llm "github.com/54c1/niq/core/llm"
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

// setActiveProvider switches this worker's active LLM provider. It must be
// called with w.mu held. The provider is built from the configured sources;
// the default LLM-summary compactor is rebound to the new provider so
// compaction follows the switch.
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
	if _, ok := w.compactor.(*DefaultCompactor); ok {
		// Rebind the default compactor (which holds an LLM provider for
		// summarization) so compaction uses the newly selected provider.
		w.compactor = NewDefaultCompactor(prov, w.keepTail)
	}
	w.resolveContextWindow()
	return nil
}

// resolveContextWindow sets w.contextWindow to the effective context-window size
// for the currently selected provider/model. An explicit params.context_window
// (already loaded into w.contextWindow) always wins; otherwise the window is
// discovered from the provider's merged model metadata (the API-reported window,
// falling back to the provider's configured context_window). Called at init and
// on every provider switch. Expects w.mu held.
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
		// Prefer the exact selected model.
		for _, m := range info.ModelDetails {
			if m.ID == model && m.ContextWindow > 0 {
				w.contextWindow = m.ContextWindow
				return
			}
		}
		// Fall back to the first model of this provider that reports a window.
		for _, m := range info.ModelDetails {
			if m.ContextWindow > 0 {
				w.contextWindow = m.ContextWindow
				return
			}
		}
	}
}
