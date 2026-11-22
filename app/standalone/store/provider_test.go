package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/provider"
)

// TestProviderStorageRoundTrip covers the file half of provider config: a
// missing file is an empty config, save/load preserves entries.
func TestProviderStorageRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	got, err := LoadProvider()
	if err != nil {
		t.Fatalf("LoadProvider (missing): %v", err)
	}
	if len(got.Providers) != 0 {
		t.Fatalf("missing file yielded %+v, want an empty config", got)
	}

	cfg := &provider.Config{Active: "a", Providers: []provider.Entry{
		{Name: "a", Type: "deepseek", APIKey: "sk-x", Model: "m1"},
	}}
	if err := SaveProvider(cfg); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	back, err := LoadProvider()
	if err != nil {
		t.Fatalf("LoadProvider: %v", err)
	}
	if back.Active != "a" || len(back.Providers) != 1 || back.Providers[0].APIKey != "sk-x" {
		t.Fatalf("roundtrip = %+v", back)
	}
}

// TestEnsureProviderExampleSeedsOnce verifies the skeleton is written only when
// missing, and never clobbers a user config.
func TestEnsureProviderExampleSeedsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.json")
	t.Setenv("NIQ_PROVIDER_CONFIG", path)

	if err := EnsureProviderExample(); err != nil {
		t.Fatalf("EnsureProviderExample: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("example file missing: %v", err)
	}
	if !strings.Contains(string(raw), "api_key") {
		t.Fatalf("example lacks the provider skeleton: %s", raw)
	}

	if err := SaveProvider(&provider.Config{Providers: []provider.Entry{
		{Name: "real", Type: "deepseek", APIKey: "sk-real"},
	}}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	if err := EnsureProviderExample(); err != nil {
		t.Fatalf("EnsureProviderExample on existing: %v", err)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "sk-real") {
		t.Fatalf("EnsureProviderExample clobbered the user config: %s", raw)
	}
}

// TestLayoutHonorsNIQHome pins the single source of path knowledge: root,
// project directory, and the two absolute-file overrides.
func TestLayoutHonorsNIQHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("NIQ_HOME", root)
	t.Setenv("NIQ_AUTH_CONFIG", "")
	t.Setenv("NIQ_PROVIDER_CONFIG", "")

	if got := Root(); got != root {
		t.Fatalf("Root = %q, want %q", got, root)
	}
	if got, want := ProjectDir("my proj/1"), filepath.Join(root, "projects", "my_proj_1"); got != want {
		t.Fatalf("ProjectDir = %q, want %q", got, want)
	}
	if got, want := ProjectPath("a"), filepath.Join(root, "projects", "a", "project.json"); got != want {
		t.Fatalf("ProjectPath = %q, want %q", got, want)
	}
	if got, want := AuthConfigPath(), filepath.Join(root, "common", "auth.json"); got != want {
		t.Fatalf("AuthConfigPath = %q, want %q", got, want)
	}
	if got, want := ProviderConfigPath(), filepath.Join(root, "common", "providers", "provider.json"); got != want {
		t.Fatalf("ProviderConfigPath = %q, want %q", got, want)
	}
	if got := LogPath("a"); !strings.HasPrefix(got, filepath.Join(root, "projects", "a", "logs")) {
		t.Fatalf("LogPath = %q", got)
	}
}
