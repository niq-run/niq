// Implementation of reason.ProviderSources backed by the providercfg package,
// so a reason worker can be handed the full provider list from provider.json
// and switch its active provider/model at runtime via worker.update.
package swarm

import (
	"fmt"

	llm "github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/providercfg"
	"github.com/54c1/niq/pkg/reason"
)

// swarmProviderSources exposes every provider configured in provider.json. The
// initial provider honors any explicit spawn-time selection (the legacy
// provider/api_key/base_url/model params) and otherwise falls back to the
// config's active/first provider.
type swarmProviderSources struct {
	// force is the explicit spawn-time selection, if any. nil selects the
	// config default.
	force *forcedProvider
}

type forcedProvider struct {
	provider string
	apiKey   string
	baseURL  string
	model    string
}

func newSwarmProviderSources(provider, apiKey, baseURL, model string) reason.ProviderSources {
	f := &forcedProvider{provider: provider, apiKey: apiKey, baseURL: baseURL, model: model}
	if provider == "" && apiKey == "" && baseURL == "" && model == "" {
		return &swarmProviderSources{force: nil}
	}
	return &swarmProviderSources{force: f}
}

// Default returns the initially active provider: the explicit spawn-time
// selection when given, otherwise the config's active (or first) provider.
func (s *swarmProviderSources) Default() llm.LLMProvider {
	if s.force != nil {
		return providerFromArgs(s.force.provider, s.force.apiKey, s.force.baseURL, s.force.model)
	}
	p, ok := providercfg.Default()
	if !ok {
		return nil
	}
	return providercfg.Build(p)
}

// Build constructs a provider bound to name and an explicit model (empty uses
// the provider's default). The name must resolve to a configured provider and
// the model must be in its list.
func (s *swarmProviderSources) Build(name, model string) (llm.LLMProvider, error) {
	p, ok := providercfg.Find(name)
	if !ok {
		return nil, fmt.Errorf("swarm: unknown provider %q", name)
	}
	if model != "" && !modelContains(p.ListModels(), model) {
		return nil, fmt.Errorf("swarm: model %q not available for provider %q", model, name)
	}
	return providercfg.BuildWithOverrides(p, "", "", model), nil
}

// List enumerates every configured provider and its models.
func (s *swarmProviderSources) List() []reason.ProviderInfo {
	cfg, err := providercfg.Load()
	if err != nil {
		return nil
	}
	out := make([]reason.ProviderInfo, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		out = append(out, reason.ProviderInfo{
			Name:    p.Name,
			Default: p.ResolveDefaultModel(),
			Models:  p.ListModels(),
		})
	}
	return out
}

func modelContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}