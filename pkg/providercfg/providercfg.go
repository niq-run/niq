// Package providercfg loads LLM provider configuration from ~/.niq/provider.json.
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
package providercfg

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/54c1/niq/core/llm"
	"github.com/54c1/niq/pkg/llmprovider/anthropic"
	"github.com/54c1/niq/pkg/llmprovider/openai"
	"github.com/54c1/niq/pkg/llmprovider/openairesponses"
)

// Provider describes one named LLM provider.
//
// Model is the default model used when none is selected explicitly; Models is
// the full list of models the provider offers. When Model is empty and Models is
// non-empty, Model falls back to Models[0] (see ResolveDefaultModel).
type Provider struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Model   string   `json:"model,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// ListModels returns the provider's full model list. When Models is empty it
// transparently falls back to a single-element list [Model].
func (p Provider) ListModels() []string {
	if len(p.Models) > 0 {
		return p.Models
	}
	if p.Model != "" {
		return []string{p.Model}
	}
	return nil
}

// ResolveDefaultModel returns the effective default model for the provider,
// falling back to the first entry of Models when Model is unset.
func (p Provider) ResolveDefaultModel() string {
	if p.Model != "" {
		return p.Model
	}
	if len(p.Models) > 0 {
		return p.Models[0]
	}
	return ""
}

// Config is the top-level provider.json shape.
type Config struct {
	Active    string     `json:"active,omitempty"`
	Providers []Provider `json:"providers"`
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
		return nil, fmt.Errorf("providercfg: read %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("providercfg: parse %s: %w", path, err)
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
		return fmt.Errorf("providercfg: mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(exampleJSON), 0644); err != nil {
		return fmt.Errorf("providercfg: write example %s: %w", path, err)
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
func Default() (Provider, bool) {
	cfg, err := Load()
	if err != nil || len(cfg.Providers) == 0 {
		return Provider{}, false
	}
	if cfg.Active != "" {
		if p, ok := findByName(cfg, cfg.Active); ok {
			return p, true
		}
	}
	return cfg.Providers[0], true
}

// Find returns a provider by name.
func Find(name string) (Provider, bool) {
	cfg, err := Load()
	if err != nil {
		return Provider{}, false
	}
	return findByName(cfg, name)
}

// FindByType returns the first provider with the given type.
func FindByType(typ string) (Provider, bool) {
	cfg, err := Load()
	if err != nil {
		return Provider{}, false
	}
	for _, p := range cfg.Providers {
		if strings.EqualFold(p.Type, typ) {
			return p, true
		}
	}
	return Provider{}, false
}

// Switch updates the active provider. It is intended to back UI-driven
// provider switching later.
func Switch(name string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	if _, ok := findByName(cfg, name); !ok {
		return fmt.Errorf("providercfg: provider %q not found", name)
	}
	cfg.Active = name
	return Write(cfg)
}

// Write persists a provider config.
func Write(cfg *Config) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("providercfg: mkdir: %w", err)
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("providercfg: marshal: %w", err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return fmt.Errorf("providercfg: write %s: %w", path, err)
	}
	return nil
}

// Build constructs an llm.LLMProvider from a Provider config. The model used is
// the provider's default model (see ResolveDefaultModel); a caller that wants
// another model from Models should pass it via BuildWithOverrides.
func Build(p Provider) llm.LLMProvider {
	if resolved := p.ResolveDefaultModel(); resolved != "" {
		p.Model = resolved
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
		})
	}
}

// SetModel updates a provider's default model. The model must appear in the
// provider's Models list (or match its current single Model when no list is
// configured). Persists the updated config.
func SetModel(name, model string) error {
	cfg, err := Load()
	if err != nil {
		return err
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Name != name {
			continue
		}
		if !contains(p.ListModels(), model) {
			return fmt.Errorf("providercfg: model %q not available for provider %q", model, name)
		}
		p.Model = model
		return Write(cfg)
	}
	return fmt.Errorf("providercfg: provider %q not found", name)
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// BuildWithOverrides constructs a provider but lets caller-supplied values win
// over values from provider.json.
func BuildWithOverrides(p Provider, apiKey, baseURL, model string) llm.LLMProvider {
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

func findByName(cfg *Config, name string) (Provider, bool) {
	for _, p := range cfg.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}
