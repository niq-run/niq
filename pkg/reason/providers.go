package reason

import (
	"errors"

	llm "github.com/54c1/niq/core/llm"
)

// ProviderInfo describes one selectable provider a reason worker can switch to
// at runtime: its name, the default model, and the full model list.
type ProviderInfo struct {
	Name    string
	Default string
	Models  []string
}

// ProviderSources is the runtime selection point for a reason worker's LLM
// provider. A worker can be given many providers (instead of a single baked-in
// one) and switch the active provider/model later via worker.update without
// depending on any concrete provider or config package.
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
	if _, ok := w.compactor.(*DefaultCompactor); ok {
		// Rebind the default compactor (which holds an LLM provider for
		// summarization) so compaction uses the newly selected provider.
		w.compactor = NewDefaultCompactor(prov, w.keepTail)
	}
	return nil
}