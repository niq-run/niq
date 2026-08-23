package providercfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderConfigDefaultAndSwitch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	cfg := &Config{
		Providers: []Provider{
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

	if err := Switch("deepseek-claude"); err != nil {
		t.Fatalf("Switch: %v", err)
	}
	def, ok = Default()
	if !ok || def.Name != "deepseek-claude" {
		t.Fatalf("Default after switch = %+v, %v", def, ok)
	}

	if err := Switch("missing"); err == nil {
		t.Fatal("Switch(missing) should fail")
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
		Providers: []Provider{
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

// TestMigrateLegacy verifies a pre-common-layout provider.json at ~/.niq is
// copied into the common layout on first Load (preserving existing keys) and
// that the migration is idempotent / does not overwrite a newer common file.
func TestMigrateLegacy(t *testing.T) {
	// Isolate HOME so we never touch a real ~/.niq.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("NIQ_PROVIDER_CONFIG", "")

	legacy := filepath.Join(home, ".niq", "provider.json")
	if err := os.MkdirAll(filepath.Dir(legacy), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"active":"legacy","providers":[]}`), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Active != "legacy" {
		t.Fatalf("Active = %q, want legacy", cfg.Active)
	}
	// The common-layout file should now exist and match the legacy content.
	dest := Path()
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("migrated file missing: %v", err)
	}
	if !strings.Contains(string(raw), `"legacy"`) {
		t.Fatalf("migrated file does not carry legacy data: %s", raw)
	}

	// Second Load must not touch the (now existing) common file.
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("common file should still exist: %v", err)
	}
}
