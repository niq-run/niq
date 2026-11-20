package provider

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestProviderConfigDefaultAndFind(t *testing.T) {
	cfg := &Config{
		Providers: []Entry{
			{Name: "deepseek-responses", Type: "openai-responses", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash"},
			{Name: "deepseek-claude", Type: "claude", BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-v4-flash"},
		},
	}

	def, ok := Default(cfg)
	if !ok || def.Name != "deepseek-responses" {
		t.Fatalf("Default = %+v, %v", def, ok)
	}

	claude, ok := Find(cfg, "deepseek-claude")
	if !ok || claude.Type != "claude" {
		t.Fatalf("Find = %+v, %v", claude, ok)
	}
	if _, ok := Find(cfg, "missing"); ok {
		t.Fatal("Find returned a provider that is not configured")
	}
}

func TestProviderConfigEmptyAndStaleActive(t *testing.T) {
	if _, ok := Default(&Config{}); ok {
		t.Fatal("an empty config has no default provider")
	}
	if _, ok := Default(nil); ok {
		t.Fatal("a nil config has no default provider")
	}

	cfg := &Config{
		Active: "stale",
		Providers: []Entry{
			{Name: "first", Type: "openai"},
			{Name: "second", Type: "claude"},
		},
	}
	def, ok := Default(cfg)
	if !ok || def.Name != "first" {
		t.Fatalf("Default = %+v, %v; a stale active falls back to the first", def, ok)
	}
}

// TestConfigured verifies the gate: the example skeleton (empty api_key) does
// not count, one provider with a key does.
func TestConfigured(t *testing.T) {
	if Configured(&Config{}) {
		t.Fatal("empty config is not configured")
	}
	if Configured(&Config{Providers: []Entry{{Name: "example", Type: "deepseek"}}}) {
		t.Fatal("a provider without an api_key is not configured")
	}
	if !Configured(&Config{Providers: []Entry{{Name: "real", Type: "deepseek", APIKey: "sk-x"}}}) {
		t.Fatal("a provider with an api_key is configured")
	}
}

// TestExampleTemplate pins the built-in default template: exactly one deepseek
// provider with an empty api_key (the user fills one in) and its model list
// carrying the per-model context window.
func TestExampleTemplate(t *testing.T) {
	cfg := Example()
	if cfg.Active != "deepseek" || len(cfg.Providers) != 1 {
		t.Fatalf("example = %+v, want one active deepseek provider", cfg)
	}
	p := cfg.Providers[0]
	if p.Name != "deepseek" || p.Type != "deepseek" {
		t.Fatalf("provider = %q/%q, want deepseek/deepseek", p.Name, p.Type)
	}
	if p.APIKey != "" {
		t.Fatalf("example must not ship an api_key, got %q", p.APIKey)
	}
	if p.BaseURL != "https://api.deepseek.com/v1" {
		t.Fatalf("base_url = %q", p.BaseURL)
	}
	if len(p.Models) != 1 || p.Models[0].Name != "deepseek-flash" || p.Models[0].ContextWindow != 1024000 {
		t.Fatalf("models = %+v, want deepseek-flash with a 1024000 context window", p.Models)
	}
}

// TestModelsMarshalKeepsMetadata verifies a model carrying only max_output or
// input modalities still serializes as an object: the compact string form is
// only for names without metadata, and writing one back as a bare string would
// silently drop the declared capability.
func TestModelsMarshalKeepsMetadata(t *testing.T) {
	cases := []Models{
		{{Name: "a", MaxOutput: 8192}},
		{{Name: "b", InputTypes: []string{"text", "image", "audio"}}},
		{{Name: "c", ContextWindow: 128000, MaxOutput: 4096, InputTypes: []string{"text", "video"}}},
	}
	for _, in := range cases {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.HasPrefix(string(raw), "[{") {
			t.Fatalf("metadata was flattened to names: %s", raw)
		}
		var back Models
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(back) != 1 || back[0].Name != in[0].Name ||
			back[0].ContextWindow != in[0].ContextWindow ||
			back[0].MaxOutput != in[0].MaxOutput ||
			!slices.Equal(back[0].InputTypes, in[0].InputTypes) {
			t.Fatalf("roundtrip lost metadata: %s -> %+v", raw, back)
		}
	}
}
