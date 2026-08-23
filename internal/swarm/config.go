// Package swarm provides the config-driven assembly for niq swarm.
//
// It reads a YAML config, instantiates workers, registers them with
// WorkerService, and manages the lifecycle. This is the only place where
// config files are parsed into worker instances — no new abstractions.
package swarm

import (
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

//go:embed preset/*.yaml
var presetFS embed.FS

// SwarmConfig is the top-level structure of a swarm YAML config file.
type SwarmConfig struct {
	Workers []WorkerConfig `yaml:"workers"`
}

// WorkerConfig describes a single worker instance declaration.
type WorkerConfig struct {
	Type          string   `yaml:"type"` // reason / workspace / host / timer / hiw
	ID            string   `yaml:"id"`
	Instruction   string   `yaml:"instruction,omitempty"`
	Provider      string   `yaml:"provider,omitempty"`
	APIKey        string   `yaml:"api_key,omitempty"`
	BaseURL       string   `yaml:"base_url,omitempty"`
	Model         string   `yaml:"model,omitempty"`
	Subscriptions []string `yaml:"subscriptions,omitempty"`
	Publish       []string `yaml:"publish,omitempty"`
	RootDir       string   `yaml:"root_dir,omitempty"`
}

// ParseConfig reads and parses a swarm YAML config file.
func ParseConfig(path string) (*SwarmConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("swarm: read config: %w", err)
	}
	return parseYAML(raw)
}

// LoadPreset loads a built-in preset by name (without the .yaml suffix).
func LoadPreset(name string) (*SwarmConfig, error) {
	raw, err := presetFS.ReadFile("preset/" + name + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("swarm: preset %q not found", name)
	}
	return parseYAML(raw)
}

// TemplatesDir returns the on-disk template directory under the shared
// "common" layer: ~/.niq/common/templates. Templates are seeded here from the
// built-ins on first run and become user-editable files from then on.
func TemplatesDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq", "common", "templates")
}

// SeedTemplates copies the built-in preset templates to dir, but only when dir
// has no .yaml yet (first-run seeding). Idempotent, best-effort on empty dirs.
func SeedTemplates(dir string) error {
	if dir == "" {
		return nil
	}
	existing, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
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

// LoadTemplate loads a project template from the on-disk templates dir,
// preferring the disk copy over the embedded builtin. The disk copy is what a
// user edits; the embedded one is the fallback before seeding.
func LoadTemplate(dir, name string) (*SwarmConfig, error) {
	if dir != "" {
		raw, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
		if err == nil {
			return parseYAML(raw)
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("swarm: read template %s: %w", name, err)
		}
	}
	return LoadPreset(name)
}

func parseYAML(raw []byte) (*SwarmConfig, error) {
	var cfg SwarmConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("swarm: parse config: %w", err)
	}
	for i, w := range cfg.Workers {
		if w.Type == "" {
			return nil, fmt.Errorf("swarm: worker %d: type is required", i)
		}
		if w.ID == "" {
			return nil, fmt.Errorf("swarm: worker %d: id is required", i)
		}
	}
	return &cfg, nil
}
