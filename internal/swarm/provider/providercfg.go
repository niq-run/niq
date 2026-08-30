// Package provider loads LLM provider configuration from ~/.niq/provider.json.
//
// The file is optional. When present, the first provider is the default unless
// the active field names a provider explicitly:
//
//	{
//	  "active": "deepseek-responses",
//	  "providers": [
//	    {"name": "deepseek-responses", "type": "openai-responses", "base_url": "https://api.deepseek.com", "model": "deepseek-v4-flash"},
//	    {"name": "deepseek-claude", "type": "claude", "base_url": "https://api.deepseek.com/anthropic", "model": "deepseek-v4-flash"}
//	  ]
//	}
//
// Each provider may also carry custom authentication headers:
//
//	{"name": "acme", "type": "openai-compatible", "base_url": "https://llm.acme.dev/v1",
//	 "api_key": "${ACME_KEY}",
//	 "headers": {"X-Tenant": "team-a", "Authorization": "Bearer ${ACME_KEY}"}}
//
// api_key and header values support ${VAR} / $VAR environment-variable
// expansion; custom headers are applied after the built-in auth headers and
// may therefore override them.
package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/pkg/llmprovider/anthropic"
	"github.com/niq-run/niq/pkg/llmprovider/openai"
	"github.com/niq-run/niq/pkg/llmprovider/openairesponses"
)

// Entry describes one named LLM provider.
//
// Model is the default model used when none is selected explicitly; Models is
// the full list of models the provider offers, each with optional per-model
// metadata such as ContextWindow. When Model is empty and Models is non-empty,
// Model falls back to Models[0] (see ResolveDefaultModel).
type Entry struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	APIKey  string            `json:"api_key,omitempty"`
	BaseURL string            `json:"base_url,omitempty"`
	Model   string            `json:"model,omitempty"`
	Models  Models            `json:"models,omitempty"`
	// ContextWindow is the default context-window size (in tokens) applied to
	// models that do not declare their own (see ModelSpec.ContextWindow) and
	// when the model API does not report one. It backs the reason worker's
	// context-budget thresholds.
	ContextWindow int `json:"context_window,omitempty"`
	// Headers are extra HTTP headers sent on every provider request, applied
	// after the built-in auth headers so they may override them. Values (and
	// api_key) support ${VAR} / $VAR environment-variable expansion.
	Headers map[string]string `json:"headers,omitempty"`
}

// ModelSpec describes one model offered by a provider, with optional per-model
// metadata. ContextWindow is the model's context-window size in tokens; a zero
// value means "unspecified" and defers to the provider's ContextWindow or the
// model API.
type ModelSpec struct {
	Name          string `json:"name"`
	ContextWindow int    `json:"context_window,omitempty"`
}

// Models is the provider's model list. It accepts both a JSON array of strings
// (model names, the legacy form) and an array of objects
// {"name", "context_window"} for per-model metadata, so existing string-array
// configs keep working.
type Models []ModelSpec

// UnmarshalJSON accepts either ["a", "b"] or [{"name": "a", "context_window": 8}].
func (m *Models) UnmarshalJSON(data []byte) error {
	var names []string
	if err := json.Unmarshal(data, &names); err == nil {
		*m = make(Models, len(names))
		for i, n := range names {
			(*m)[i] = ModelSpec{Name: n}
		}
		return nil
	}
	var specs []ModelSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return err
	}
	*m = specs
	return nil
}

// MarshalJSON emits a plain string array when no model carries metadata, keeping
// configs compact and backward compatible on write; otherwise emits objects.
func (m Models) MarshalJSON() ([]byte, error) {
	hasMeta := false
	for _, s := range m {
		if s.ContextWindow != 0 {
			hasMeta = true
			break
		}
	}
	if !hasMeta {
		names := make([]string, 0, len(m))
		for _, s := range m {
			names = append(names, s.Name)
		}
		return json.Marshal(names)
	}
	type spec ModelSpec // alias to avoid infinite recursion
	specs := make([]spec, 0, len(m))
	for _, s := range m {
		specs = append(specs, spec(s))
	}
	return json.Marshal(specs)
}

// ListModels returns the provider's full model list. When Models is empty it
// transparently falls back to a single-element list [Model].
func (p Entry) ListModels() []string {
	if len(p.Models) > 0 {
		names := make([]string, 0, len(p.Models))
		for _, m := range p.Models {
			names = append(names, m.Name)
		}
		return names
	}
	if p.Model != "" {
		return []string{p.Model}
	}
	return nil
}

// ResolveDefaultModel returns the effective default model for the provider,
// falling back to the first entry of Models when Model is unset.
func (p Entry) ResolveDefaultModel() string {
	if p.Model != "" {
		return p.Model
	}
	if len(p.Models) > 0 {
		return p.Models[0].Name
	}
	return ""
}

// Config is the top-level provider.json shape.
type Config struct {
	Active    string  `json:"active,omitempty"`
	Providers []Entry `json:"providers"`
}

// Path returns the provider config path. NIQ_PROVIDER_CONFIG overrides the
// default ~/.niq/common/providers/provider.json.
func Path() string {
	if p := os.Getenv("NIQ_PROVIDER_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq", "common", "providers", "provider.json")
}

// Load reads the provider config. A missing file is not an error and returns
// an empty config.
func Load() (*Config, error) {
	path := Path()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("provider: read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("provider: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// exampleJSON is the skeleton provider config written by EnsureExample. The
// api_key is intentionally empty: the user fills it in before the swarm starts.
const exampleJSON = `{
  "active": "deepseek",
  "providers": [
    {
      "name": "deepseek",
      "type": "deepseek",
      "api_key": "",
      "base_url": "https://api.deepseek.com/v1",
      "model": "deepseek-v4-flash",
      "models": ["deepseek-v4-pro", "deepseek-v4-flash"]
    }
  ]
}
`

// EnsureExample writes the example provider config to Path() when the file is
// missing. It never overwrites an existing file: once a config exists it is
// user-maintained.
func EnsureExample() error {
	path := Path()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("provider: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(exampleJSON), 0644); err != nil {
		return fmt.Errorf("provider: write example %s: %w", path, err)
	}
	return nil
}

// Configured reports whether a usable provider is configured: at least one
// provider with a non-empty api_key. The example skeleton (empty api_key) does
// not count.
func Configured() bool {
	cfg, err := Load()
	if err != nil {
		return false
	}
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			return true
		}
	}
	return false
}

// Default returns the active provider, or the first configured provider when
// active is empty or stale.
func Default() (Entry, bool) {
	cfg, err := Load()
	if err != nil || len(cfg.Providers) == 0 {
		return Entry{}, false
	}
	if cfg.Active != "" {
		if p, ok := findByName(cfg, cfg.Active); ok {
			return p, true
		}
	}
	return cfg.Providers[0], true
}

// Find returns a provider by name.
func Find(name string) (Entry, bool) {
	cfg, err := Load()
	if err != nil {
		return Entry{}, false
	}
	return findByName(cfg, name)
}

// FindByType returns the first provider with the given type.
func FindByType(typ string) (Entry, bool) {
	cfg, err := Load()
	if err != nil {
		return Entry{}, false
	}
	for _, p := range cfg.Providers {
		if strings.EqualFold(p.Type, typ) {
			return p, true
		}
	}
	return Entry{}, false
}

// Write persists a provider config.
func Write(cfg *Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("provider: mkdir: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("provider: marshal: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return fmt.Errorf("provider: write %s: %w", path, err)
	}
	return nil
}

// Build constructs an llm.LLMProvider from a provider Entry. The model used is
// the entry's default model (see ResolveDefaultModel); a caller that wants
// another model from Models should pass it via BuildWithOverrides.
func Build(p Entry) llm.LLMProvider {
	if resolved := p.ResolveDefaultModel(); resolved != "" {
		p.Model = resolved
	}
	// Expand ${VAR}/$VAR in the API key and in every custom header value so
	// secrets can live in the environment rather than provider.json.
	p.APIKey = expandEnv(p.APIKey)
	headers := make(map[string]string, len(p.Headers))
	for k, v := range p.Headers {
		headers[k] = expandEnv(v)
	}
	switch p.Type {
	case "claude", "anthropic":
		if p.APIKey == "" {
			p.APIKey = os.Getenv("ANTHROPIC_API_KEY")
		}
		if p.BaseURL == "" {
			p.BaseURL = "https://api.deepseek.com/anthropic"
		}
		if p.Model == "" {
			p.Model = "claude-sonnet-4-20250514"
		}
		return anthropic.New(anthropic.Config{
			APIKey:  p.APIKey,
			BaseURL: p.BaseURL,
			Model:   p.Model,
			Headers: headers,
		})
	case "openai-responses", "responses", "deepseek-responses":
		if p.APIKey == "" {
			p.APIKey = os.Getenv("OPENAI_API_KEY")
		}
		if p.BaseURL == "" {
			p.BaseURL = "https://api.deepseek.com"
		}
		if p.Model == "" {
			p.Model = "deepseek-v4-flash"
		}
		return openairesponses.New(openairesponses.Config{
			APIKey:  p.APIKey,
			BaseURL: p.BaseURL,
			Model:   p.Model,
			Headers: headers,
		})
	case "deepseek":
		if p.APIKey == "" {
			p.APIKey = os.Getenv("OPENAI_API_KEY")
		}
		if p.BaseURL == "" {
			p.BaseURL = "https://api.deepseek.com/v1"
		}
		if p.Model == "" {
			p.Model = "deepseek-v4-flash"
		}
		return openai.New(openai.Config{
			APIKey:  p.APIKey,
			BaseURL: p.BaseURL,
			Model:   p.Model,
			Headers: headers,
		})
	default:
		if p.APIKey == "" {
			p.APIKey = os.Getenv("OPENAI_API_KEY")
		}
		if p.BaseURL == "" {
			if p.Type == "openai-compatible" {
				p.BaseURL = "https://api.deepseek.com/v1"
			} else {
				p.BaseURL = "https://api.openai.com/v1"
			}
		}
		if p.Model == "" {
			p.Model = "gpt-4o"
		}
		return openai.New(openai.Config{
			APIKey:  p.APIKey,
			BaseURL: p.BaseURL,
			Model:   p.Model,
			Headers: headers,
		})
	}
}

// BuildWithOverrides constructs a provider but lets caller-supplied values win
// over values from provider.json.
func BuildWithOverrides(p Entry, apiKey, baseURL, model string) llm.LLMProvider {
	if apiKey != "" {
		p.APIKey = apiKey
	}
	if baseURL != "" {
		p.BaseURL = baseURL
	}
	if model != "" {
		p.Model = model
	}
	return Build(p)
}

func findByName(cfg *Config, name string) (Entry, bool) {
	for _, p := range cfg.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Entry{}, false
}

// expandEnv substitutes ${VAR} and $VAR references with the named environment
// variable's value. A missing variable expands to an empty string.
func expandEnv(s string) string {
	return os.Expand(s, func(key string) string {
		return os.Getenv(key)
	})
}
