package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderConfigDefaultAndFind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	cfg := &Config{
		Providers: []Entry{
			{Name: "deepseek-responses", Type: "openai-responses", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash"},
			{Name: "deepseek-claude", Type: "claude", BaseURL: "https://api.deepseek.com/anthropic", Model: "deepseek-v4-flash"},
		},
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	def, ok := Default()
	if !ok || def.Name != "deepseek-responses" {
		t.Fatalf("Default = %+v, %v", def, ok)
	}

	claude, ok := Find("deepseek-claude")
	if !ok || claude.Type != "claude" {
		t.Fatalf("Find = %+v, %v", claude, ok)
	}

	byType, ok := FindByType("claude")
	if !ok || byType.Name != "deepseek-claude" {
		t.Fatalf("FindByType = %+v, %v", byType, ok)
	}
}

func TestProviderConfigMissingFile(t *testing.T) {
	t.Setenv("NIQ_PROVIDER_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	if _, ok := Default(); ok {
		t.Fatal("Default should be false when file is missing")
	}
}

func TestProviderConfigActiveFallsBackToFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	cfg := &Config{
		Active: "stale",
		Providers: []Entry{
			{Name: "first", Type: "openai"},
			{Name: "second", Type: "claude"},
		},
	}
	if err := Write(cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}

	def, ok := Default()
	if !ok || def.Name != "first" {
		t.Fatalf("Default = %+v, %v", def, ok)
	}
}

func TestProviderConfigPathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom-provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)
	if got := Path(); got != path {
		t.Fatalf("Path() = %q, want %q", got, path)
	}

	_ = os.Unsetenv("NIQ_PROVIDER_CONFIG")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".niq", "common", "providers", "provider.json")
	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

// TestEnsureExampleAndConfigured verifies the example skeleton is seeded only
// when missing (never overwriting a user config) and that Configured stays
// false for the empty example but true once a provider has an api_key.
func TestEnsureExampleAndConfigured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	if Configured() {
		t.Fatal("Configured should be false before EnsureExample")
	}
	if err := EnsureExample(); err != nil {
		t.Fatalf("EnsureExample: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("example file missing: %v", err)
	}
	if !strings.Contains(string(raw), "api_key") {
		t.Fatalf("example file lacks provider skeleton: %s", raw)
	}
	if Configured() {
		t.Fatal("Configured should be false for the empty example")
	}

	// EnsureExample must not overwrite an existing config.
	if err := Write(&Config{Providers: []Entry{{Name: "real", Type: "deepseek", APIKey: "sk-x"}}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := EnsureExample(); err != nil {
		t.Fatalf("EnsureExample on existing: %v", err)
	}
	if !Configured() {
		t.Fatal("Configured should be true with a real api_key")
	}
	raw2, _ := os.ReadFile(path)
	if !strings.Contains(string(raw2), "sk-x") {
		t.Fatalf("EnsureExample clobbered user config: %s", raw2)
	}
}
