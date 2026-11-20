package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/app/standalone/provider"
)

// Provider config storage: <root>/common/providers/provider.json (override:
// NIQ_PROVIDER_CONFIG). The provider package owns the shape and the semantics;
// this file owns where it lives and how it is read/written.

// LoadProvider reads provider.json. A missing file yields an empty config and
// no error, matching "no provider configured yet".
func LoadProvider() (*provider.Config, error) {
	path := ProviderConfigPath()
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &provider.Config{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", path, err)
	}
	var cfg provider.Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", path, err)
	}
	return &cfg, nil
}

// SaveProvider writes provider.json atomically, creating its directory.
func SaveProvider(cfg *provider.Config) error {
	path := ProviderConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", filepath.Dir(path), err)
	}
	return writeJSONAtomic(path, cfg)
}

// EnsureProviderExample seeds the built-in provider skeleton when no file
// exists yet. It never overwrites a user config.
func EnsureProviderExample() error {
	path := ProviderConfigPath()
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("store: mkdir %s: %w", filepath.Dir(path), err)
	}
	return writeRawAtomic(path, []byte(provider.ExampleJSON()))
}
