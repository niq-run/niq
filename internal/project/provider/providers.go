// Implementation of reason.ProviderSources backed by this package, so a reason
// worker can be handed the full provider list from provider.json and switch its
// active provider/model at runtime via worker.update.
package provider

import (
	"context"
	"fmt"
	"log"
	"time"

	llm "github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/reason"
	"github.com/niq-run/niq/pkg/services/workerhost"
)

// EnsureLLMConfigured gates project startup on an LLM provider being configured
// when the persisted worker set includes a reason worker. On a missing or
// empty config it seeds the example provider.json and returns an actionable
// error, so the project exits before any worker starts.
func EnsureLLMConfigured(svc *workerhost.WorkerService) error {
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
	if Configured() {
		return nil
	}
	if err := EnsureExample(); err != nil {
		return fmt.Errorf("provider: seed provider example: %w", err)
	}
	return fmt.Errorf("no LLM provider configured: edit %s and re-run", Path())
}

// providerSources exposes every provider configured in provider.json. The
// initial provider honors any explicit spawn-time selection (the legacy
// provider/api_key/base_url/model params) and otherwise falls back to the
// config's active/first provider.
type providerSources struct {
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

// NewProviderSources returns a reason.ProviderSources backed by
// provider.json. An all-empty selection picks the config default; any non-empty
// spawn-time argument (provider name/type, api_key, base_url, model) forces a
// specific initial provider.
func NewProviderSources(provider, apiKey, baseURL, model string) reason.ProviderSources {
	f := &forcedProvider{provider: provider, apiKey: apiKey, baseURL: baseURL, model: model}
	if provider == "" && apiKey == "" && baseURL == "" && model == "" {
		return &providerSources{force: nil}
	}
	return &providerSources{force: f}
}

// InitialProviderInfo resolves the initially active provider's name and model,
// mirroring NewProviderSources' forced-vs-default selection, so the
// reason worker can report its current choice via worker.status.
func InitialProviderInfo(provider, apiKey, baseURL, model string) (string, string) {
	if provider != "" || apiKey != "" || baseURL != "" || model != "" {
		if model == "" && provider != "" {
			if p, ok := Find(provider); ok {
				model = p.ResolveDefaultModel()
			}
		}
		return provider, model
	}
	if p, ok := Default(); ok {
		return p.Name, p.ResolveDefaultModel()
	}
	return "", ""
}

// Default returns the initially active provider: the explicit spawn-time
// selection when given, otherwise the config's active (or first) provider.
func (s *providerSources) Default() llm.LLMProvider {
	if s.force != nil {
		return providerFromArgs(s.force.provider, s.force.apiKey, s.force.baseURL, s.force.model)
	}
	p, ok := Default()
	if !ok {
		log.Printf("[project] provider sources: no default provider configured (provider.json missing or empty)")
		return nil
	}
	log.Printf("[project] resolved default provider: name=%s type=%s model=%s base_url=%s",
		p.Name, p.Type, p.ResolveDefaultModel(), p.BaseURL)
	return Build(p)
}

// Build constructs a provider bound to name and an explicit model (empty uses
// the provider's default). The name must resolve to a configured provider; the
// model is not pre-validated against the model list — an unknown model is
// rejected by the provider's API call itself, which lets models discovered only
// via the provider API (see List) be selected at runtime.
func (s *providerSources) Build(name, model string) (llm.LLMProvider, error) {
	p, ok := Find(name)
	if !ok {
		log.Printf("[project] provider switch failed: unknown provider %q", name)
		return nil, fmt.Errorf("provider: unknown provider %q", name)
	}
	effModel := model
	if effModel == "" {
		effModel = p.ResolveDefaultModel()
	}
	log.Printf("[project] building provider: name=%s type=%s model=%s base_url=%s",
		name, p.Type, effModel, p.BaseURL)
	return BuildWithOverrides(p, "", "", model), nil
}

// List enumerates every configured provider and its models. A provider with a
// non-empty configured model list reports exactly that list; otherwise the
// model list comes (best-effort) from the provider's API, and an API failure
// yields an empty list. ModelDetails carries per-model metadata (e.g.
// ContextWindow) when available.
func (s *providerSources) List() []reason.ProviderInfo {
	cfg, err := Load()
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

// listModelsTimeout bounds a single provider's live model-list call. Several
// providers are queried in sequence, so the whole provider.list round is
// roughly this times the number of configured providers.
const listModelsTimeout = 3 * time.Second

// modelsFor returns the model set for a provider as []llm.ModelInfo. A
// non-empty configured model list is authoritative: the user explicitly pinned
// the offered models (some providers report unwieldy lists), so the provider's
// API is not queried at all. Only when no models are configured does it fall
// back to a live API query. ContextWindow comes from the per-model spec or,
// failing that, the provider's configured context_window.
func (s *providerSources) modelsFor(p Entry) []llm.ModelInfo {
	// Configured list wins over discovery: serve it as-is, no API call.
	if len(p.Models) > 0 {
		details := make([]llm.ModelInfo, 0, len(p.Models))
		for _, ms := range p.Models {
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
		return details
	}

	// No models configured: discover them from the provider's API. Bound the
	// live model-list call: this runs on the reason worker's event
	// loop while it holds its lock (worker.query provider.list), and provider
	// clients are built without an HTTP timeout, so an unresponsive endpoint
	// here would stall the whole worker indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), listModelsTimeout)
	defer cancel()
	apiModels, err := Build(p).ListModels(ctx)
	if err != nil {
		log.Printf("[project] provider %q: model list from API unavailable (%v)", p.Name, err)
		return nil
	}
	details := make([]llm.ModelInfo, 0, len(apiModels))
	for _, m := range apiModels {
		// Prefer the API-reported window; fall back to the provider config.
		if m.ContextWindow == 0 {
			m.ContextWindow = p.ContextWindow
		}
		m.Provider = p.Type
		details = append(details, m)
	}
	return details
}

// providerFromArgs builds a provider from raw spawn-time args, without reading
// provider.json: a name/type first resolves to a configured provider (by name,
// then by type) whose values are overridden by any explicit args; otherwise the
// args are treated as an ad-hoc provider definition.
func providerFromArgs(provider, apiKey, baseURL, model string) llm.LLMProvider {
	if provider != "" {
		if p, ok := Find(provider); ok {
			return BuildWithOverrides(p, apiKey, baseURL, model)
		}
		if p, ok := FindByType(provider); ok {
			return BuildWithOverrides(p, apiKey, baseURL, model)
		}
		return Build(Entry{
			Type:    provider,
			APIKey:  apiKey,
			BaseURL: baseURL,
			Model:   model,
		})
	}
	if p, ok := Default(); ok {
		return BuildWithOverrides(p, apiKey, baseURL, model)
	}
	return Build(Entry{
		Type:    "deepseek",
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	})
}
