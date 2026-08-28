// Implementation of reason.ProviderSources backed by the providercfg package,
// so a reason worker can be handed the full provider list from provider.json
// and switch its active provider/model at runtime via worker.update.
package swarm

import (
	"context"
	"fmt"
	"log"

	llm "github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/providercfg"
	"github.com/54c1/niq/pkg/reason"
	"github.com/54c1/niq/pkg/service/workerhost"
)

// ensureLLMConfigured gates swarm startup on an LLM provider being configured
// when the persisted worker set includes a reason worker. On a missing or
// empty config it seeds the example provider.json and returns an actionable
// error, so the swarm exits before any worker starts.
func ensureLLMConfigured(svc *workerhost.WorkerService) error {
	recs, err := svc.LoadAllWorkers()
	if err != nil {
		return err
	}
	needsLLM := false
	for _, rec := range recs {
		if rec.Type == "reason" {
			needsLLM = true
			break
		}
	}
	if !needsLLM {
		return nil
	}
	if providercfg.Configured() {
		return nil
	}
	if err := providercfg.EnsureExample(); err != nil {
		return fmt.Errorf("swarm: seed provider example: %w", err)
	}
	return fmt.Errorf("no LLM provider configured: edit %s and re-run", providercfg.Path())
}

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

// initialProviderInfo resolves the initially active provider's name and model,
// mirroring newSwarmProviderSources' forced-vs-default selection, so the
// reason worker can report its current choice via worker.status.
func initialProviderInfo(provider, apiKey, baseURL, model string) (string, string) {
	if provider != "" || apiKey != "" || baseURL != "" || model != "" {
		if model == "" && provider != "" {
			if p, ok := providercfg.Find(provider); ok {
				model = p.ResolveDefaultModel()
			}
		}
		return provider, model
	}
	if p, ok := providercfg.Default(); ok {
		return p.Name, p.ResolveDefaultModel()
	}
	return "", ""
}

// Default returns the initially active provider: the explicit spawn-time
// selection when given, otherwise the config's active (or first) provider.
func (s *swarmProviderSources) Default() llm.LLMProvider {
	if s.force != nil {
		return providerFromArgs(s.force.provider, s.force.apiKey, s.force.baseURL, s.force.model)
	}
	p, ok := providercfg.Default()
	if !ok {
		log.Printf("[swarm] provider sources: no default provider configured (provider.json missing or empty)")
		return nil
	}
	log.Printf("[swarm] resolved default provider: name=%s type=%s model=%s base_url=%s",
		p.Name, p.Type, p.ResolveDefaultModel(), p.BaseURL)
	return providercfg.Build(p)
}

// Build constructs a provider bound to name and an explicit model (empty uses
// the provider's default). The name must resolve to a configured provider; the
// model is not pre-validated against the model list — an unknown model is
// rejected by the provider's API call itself, which lets models discovered only
// via the provider API (see List) be selected at runtime.
func (s *swarmProviderSources) Build(name, model string) (llm.LLMProvider, error) {
	p, ok := providercfg.Find(name)
	if !ok {
		log.Printf("[swarm] provider switch failed: unknown provider %q", name)
		return nil, fmt.Errorf("swarm: unknown provider %q", name)
	}
	effModel := model
	if effModel == "" {
		effModel = p.ResolveDefaultModel()
	}
	log.Printf("[swarm] building provider: name=%s type=%s model=%s base_url=%s",
		name, p.Type, effModel, p.BaseURL)
	return providercfg.BuildWithOverrides(p, "", "", model), nil
}

// List enumerates every configured provider and its models. The model list is
// the configured models merged (best-effort) with the models reported by the
// provider's API; an API failure falls back to the configured list alone.
// ModelDetails carries per-model metadata (e.g. ContextWindow) when available.
func (s *swarmProviderSources) List() []reason.ProviderInfo {
	cfg, err := providercfg.Load()
	if err != nil {
		return nil
	}
	out := make([]reason.ProviderInfo, 0, len(cfg.Providers))
	for _, p := range cfg.Providers {
		details := s.modelsFor(p)
		ids := make([]string, 0, len(details))
		for _, m := range details {
			ids = append(ids, m.ID)
		}
		out = append(out, reason.ProviderInfo{
			Name:         p.Name,
			Default:      p.ResolveDefaultModel(),
			Models:       ids,
			ModelDetails: details,
		})
	}
	return out
}

// modelsFor returns the merged model set for a provider as []llm.ModelInfo:
// configured models first, then any API-reported models not already present.
// ContextWindow is taken from the API when reported, otherwise falls back to
// the provider's configured context_window. If the provider API is
// unreachable or errors, the configured models (with config context window) are
// returned so the provider list query still works offline.
func (s *swarmProviderSources) modelsFor(p providercfg.Provider) []llm.ModelInfo {
	configModels := p.Models
	details := make([]llm.ModelInfo, 0, len(configModels))
	seen := make(map[string]struct{}, len(configModels))
	for _, ms := range configModels {
		seen[ms.Name] = struct{}{}
		cw := ms.ContextWindow
		if cw == 0 {
			cw = p.ContextWindow
		}
		details = append(details, llm.ModelInfo{
			ID:            ms.Name,
			Name:          ms.Name,
			Provider:      p.Type,
			ContextWindow: cw,
		})
	}
	apiModels, err := providercfg.Build(p).ListModels(context.Background())
	if err != nil {
		log.Printf("[swarm] provider %q: model list from API unavailable (%v), using configured list", p.Name, err)
		return details
	}
	for _, m := range apiModels {
		if _, ok := seen[m.ID]; ok {
			continue
		}
		seen[m.ID] = struct{}{}
		// Prefer the API-reported window; fall back to the provider config.
		if m.ContextWindow == 0 {
			m.ContextWindow = p.ContextWindow
		}
		m.Provider = p.Type
		details = append(details, m)
	}
	return details
}
