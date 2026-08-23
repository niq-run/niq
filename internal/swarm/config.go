// Package swarm provides the config-driven assembly for niq swarm.
//
// It reads a JSON config, instantiates workers, registers them with
// WorkerService, and manages the lifecycle. This is the only place where
// config files are parsed into worker instances — no new abstractions.
package swarm

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed preset/*.json
var presetFS embed.FS

// SwarmConfig is the top-level structure of a swarm config file.
type SwarmConfig struct {
	Workers []WorkerConfig `json:"workers" yaml:"workers"`
}

// WorkerConfig describes a single worker instance declaration. JSON is the
// template/config format; yaml tags keep legacy --config files working.
type WorkerConfig struct {
	Type          string   `json:"type" yaml:"type"` // reason / workspace / host / timer / hiw
	ID            string   `json:"id" yaml:"id"`
	Instruction   string   `json:"instruction,omitempty" yaml:"instruction,omitempty"`
	Provider      string   `json:"provider,omitempty" yaml:"provider,omitempty"`
	APIKey        string   `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	BaseURL       string   `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	Model         string   `json:"model,omitempty" yaml:"model,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty" yaml:"subscriptions,omitempty"`
	Publish       []string `json:"publish,omitempty" yaml:"publish,omitempty"`
	RootDir       string   `json:"root_dir,omitempty" yaml:"root_dir,omitempty"`
	Archived      bool     `json:"archived,omitempty" yaml:"archived,omitempty"`
}

// ParseConfig reads and parses a swarm config file (JSON preferred, YAML accepted).
func ParseConfig(path string) (*SwarmConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("swarm: read config: %w", err)
	}
	return parseConfig(raw)
}

// LoadPreset loads a built-in preset by name (without the .json suffix).
func LoadPreset(name string) (*SwarmConfig, error) {
	raw, err := presetFS.ReadFile("preset/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("swarm: preset %q not found", name)
	}
	return parseConfig(raw)
}

// TemplatesDir returns the on-disk template directory under the shared
// "common" layer: ~/.niq/common/templates. Templates are seeded here from the
// built-ins on first run and become user-editable files from then on.
func TemplatesDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq", "common", "templates")
}

// SeedTemplates copies the built-in preset templates to dir, but only when dir
// has no .json yet (first-run seeding). Idempotent, best-effort on empty dirs.
func SeedTemplates(dir string) error {
	if dir == "" {
		return nil
	}
	existing, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(existing) > 0 {
		return nil // already seeded: never clobber user-edited templates
	}
	entries, err := presetFS.ReadDir("preset")
	if err != nil {
		return fmt.Errorf("swarm: read builtin templates: %w", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("swarm: mkdir templates %s: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		raw, err := presetFS.ReadFile("preset/" + name)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0644); err != nil {
			log.Printf("[swarm] seed template %s: %v", name, err)
		}
	}
	return nil
}

// TemplatePath returns the on-disk path of a template file under the templates dir.
func TemplatePath(dir, name string) string {
	return filepath.Join(dir, name+".json")
}

// ReadTemplateRaw returns the raw JSON for a template, preferring the on-disk
// copy over the embedded built-in (used to clone templates).
func ReadTemplateRaw(dir, name string) ([]byte, error) {
	if dir != "" {
		if b, err := os.ReadFile(TemplatePath(dir, name)); err == nil {
			return b, nil
		}
	}
	return presetFS.ReadFile("preset/" + name + ".json")
}

// ListTemplates returns the available project template names (without the
// .json suffix), preferring the on-disk common/templates dir (seeded + user
// editable) and falling back to the embedded built-ins.
func ListTemplates() ([]string, error) {
	if files, err := filepath.Glob(filepath.Join(TemplatesDir(), "*.json")); err == nil && len(files) > 0 {
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, strings.TrimSuffix(filepath.Base(f), ".json"))
		}
		sort.Strings(names)
		return names, nil
	}
	entries, err := presetFS.ReadDir("preset")
	if err != nil {
		return nil, fmt.Errorf("swarm: read embedded templates: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(names)
	return names, nil
}

// LoadTemplate loads a project template from the on-disk templates dir,
// preferring the disk copy over the embedded built-in. The disk copy is what a
// user edits; the embedded one is the fallback before seeding.
func LoadTemplate(dir, name string) (*SwarmConfig, error) {
	if dir != "" {
		raw, err := os.ReadFile(TemplatePath(dir, name))
		if err == nil {
			return parseConfig(raw)
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("swarm: read template %s: %w", name, err)
		}
	}
	return LoadPreset(name)
}

// parseConfig parses a config/template. JSON is the canonical format; YAML is
// still accepted for backwards compatibility (e.g. an old --config file).
func parseConfig(raw []byte) (*SwarmConfig, error) {
	var cfg SwarmConfig
	if err := json.Unmarshal(raw, &cfg); err == nil {
		return validateWorkers(&cfg)
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("swarm: parse config: %w", err)
	}
	return validateWorkers(&cfg)
}

func validateWorkers(cfg *SwarmConfig) (*SwarmConfig, error) {
	for i, w := range cfg.Workers {
		if w.Type == "" {
			return nil, fmt.Errorf("swarm: worker %d: type is required", i)
		}
		if w.ID == "" {
			return nil, fmt.Errorf("swarm: worker %d: id is required", i)
		}
	}
	return cfg, nil
}
