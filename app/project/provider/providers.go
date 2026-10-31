// Implementation of reason.ProviderSources backed by this package, so a reason
// worker can be handed the full provider list from provider.json and switch its
// active provider/model at runtime via worker.update.
package provider

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/niq-run/niq/core/impl/reason"
	llm "github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/worker"
)

// EnsureLLMConfigured gates project startup on an LLM provider being configured
// when the declared worker set includes a reason worker. On a missing or
// empty config it seeds the example provider.json and returns an actionable
// error, so the project exits before any worker starts.
func EnsureLLMConfigured(cfgs []worker.WorkerConfig) error {
	needsLLM := false
	for _, cfg := range cfgs {
		// Reason-family workers (reason / coding) both need an LLM.
		if cfg.Type == "reason" || cfg.Type == "coding" {
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

// providerSources exposes every provider configured in provider.json and
// resolves the initially active one. A worker's spawn-time choice is a NAME
// from that file plus one of its models — never a bare endpoint or key: the
// entry owns the credentials, so rotating them is one edit in one place.
type providerSources struct {
	// name is the spawn-time provider selection ("" = the config default).
	name string
	// model is the spawn-time model selection ("" = the provider's default).
	model string
}

// NewProviderSources returns a reason.ProviderSources backed by provider.json.
// An empty name picks the config default (active/first); an empty model picks
// the resolved provider's default model.
func NewProviderSources(name, model string) reason.ProviderSources {
	return &providerSources{name: name, model: model}
}

// InitialProviderInfo resolves the initially active provider's name and model,
// mirroring NewProviderSources' selection, so the reason worker can report its
// current choice via worker.status. An unknown name resolves to ("", "") —
// the builder rejects it before a worker is ever constructed.
func InitialProviderInfo(name, model string) (string, string) {
	if name != "" {
		p, ok := Find(name)
		if !ok {
			return "", ""
		}
		if model == "" {
			model = p.ResolveDefaultModel()
		}
		return p.Name, model
	}
	p, ok := Default()
	if !ok {
		return "", ""
	}
	if model == "" {
		model = p.ResolveDefaultModel()
	}
	return p.Name, model
}

// Default returns the initially active provider: the spawn-time selection when
// given, otherwise the config's active (or first) provider.
func (s *providerSources) Default() llm.LLMProvider {
	p, ok := s.initial()
	if !ok {
		log.Printf("[project] provider sources: no provider %q (provider.json missing or empty)", s.name)
		return nil
	}
	if s.model != "" {
		p.Model = s.model
	}
	log.Printf("[project] resolved default provider: name=%s type=%s model=%s base_url=%s",
		p.Name, p.Type, p.ResolveDefaultModel(), p.BaseURL)
	return Build(p)
}

// initial returns the configured entry the worker starts on.
func (s *providerSources) initial() (Entry, bool) {
	if s.name != "" {
		return Find(s.name)
	}
	return Default()
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
	p.Model = effModel
	return Build(p), nil
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
// back to a live API query. Per-model metadata (context window, max output,
// input modalities) comes from the model spec or, failing that, the provider
// level defaults.
func (s *providerSources) modelsFor(p Entry) []llm.ModelInfo {
	// Configured list wins over discovery: serve it as-is, no API call.
	if len(p.Models) > 0 {
		details := make([]llm.ModelInfo, 0, len(p.Models))
		for _, ms := range p.Models {
			details = append(details, llm.ModelInfo{
				ID:            ms.Name,
				Name:          ms.Name,
				Provider:      p.Type,
				ContextWindow: firstPositive(ms.ContextWindow, p.ContextWindow),
				MaxOutput:     firstPositive(ms.MaxOutput, p.MaxOutput),
				InputTypes:    firstNonEmpty(ms.InputTypes, p.InputTypes),
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
		// Prefer the API-reported metadata; fall back to the provider config.
		m.ContextWindow = firstPositive(m.ContextWindow, p.ContextWindow)
		m.MaxOutput = firstPositive(m.MaxOutput, p.MaxOutput)
		m.InputTypes = firstNonEmpty(m.InputTypes, p.InputTypes)
		m.Provider = p.Type
		details = append(details, m)
	}
	return details
}

// firstPositive returns v when set, else the fallback.
func firstPositive(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}

// firstNonEmpty returns v when it declares anything, else the fallback.
func firstNonEmpty(v, fallback []string) []string {
	if len(v) > 0 {
		return v
	}
	return fallback
}
