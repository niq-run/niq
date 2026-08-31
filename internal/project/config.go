// Package project provides the storage and runtime for niq projects.
//
// A project lives under ~/.niq/projects/<id>/ and owns its own event bus,
// WebUI, worker set and state directories. This package reads project
// templates (JSON), instantiates workers, registers them with WorkerService,
// and manages the project instance lifecycle. This is the only place where
// template files are parsed into worker instances — no new abstractions.
package project

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed preset/*.json
var presetFS embed.FS

// TemplateConfig is the top-level structure of a project template file: the
// set of workers a new project is seeded with.
type TemplateConfig struct {
	Workers []WorkerConfig `json:"workers"`
}

// WorkerConfig describes a single worker instance declaration.
type WorkerConfig struct {
	Type          string   `json:"type"` // reason / workspace / host / timer / hiw / program
	ID            string   `json:"id"`
	Instruction   string   `json:"instruction,omitempty"`
	Provider      string   `json:"provider,omitempty"`
	APIKey        string   `json:"api_key,omitempty"`
	BaseURL       string   `json:"base_url,omitempty"`
	Model         string   `json:"model,omitempty"`
	Subscriptions []string `json:"subscriptions,omitempty"`
	Publish       []string `json:"publish,omitempty"`
	RootDir       string   `json:"root_dir,omitempty"`
	Archived      bool     `json:"archived,omitempty"`
	// Managed marks the worker as host-managed (in-process, worker dir is the
	// config authority). nil or true = managed; false = an external process
	// launched by the project via Command/Env/Cwd.
	Managed    *bool             `json:"managed,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
}

// LoadPreset loads a built-in template by name (without the .json suffix).
func LoadPreset(name string) (*TemplateConfig, error) {
	raw, err := presetFS.ReadFile("preset/" + name + ".json")
	if err != nil {
		return nil, fmt.Errorf("project: preset %q not found", name)
	}
	return parseConfig(raw)
}

// parseConfig parses a JSON config/template.
func parseConfig(raw []byte) (*TemplateConfig, error) {
	var cfg TemplateConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("project: parse config: %w", err)
	}
	return validateWorkers(&cfg)
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
		return fmt.Errorf("project: read builtin templates: %w", err)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("project: mkdir templates %s: %w", dir, err)
	}
	for _, e := range entries {
		name := e.Name()
		raw, err := presetFS.ReadFile("preset/" + name)
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0644); err != nil {
			log.Printf("[project] seed template %s: %v", name, err)
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
		return nil, fmt.Errorf("project: read embedded templates: %w", err)
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
func LoadTemplate(dir, name string) (*TemplateConfig, error) {
	if dir != "" {
		raw, err := os.ReadFile(TemplatePath(dir, name))
		if err == nil {
			return parseConfig(raw)
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("project: read template %s: %w", name, err)
		}
	}
	return LoadPreset(name)
}

// workerConfigParams converts a WorkerConfig into the Params map consumed by
// the builders.
func workerConfigParams(wc WorkerConfig) map[string]any {
	p := map[string]any{}
	if wc.Instruction != "" {
		p["instruction"] = wc.Instruction
	}
	if wc.Provider != "" {
		p["provider"] = wc.Provider
	}
	if wc.APIKey != "" {
		p["api_key"] = wc.APIKey
	}
	if wc.BaseURL != "" {
		p["base_url"] = wc.BaseURL
	}
	if wc.Model != "" {
		p["model"] = wc.Model
	}
	if len(wc.Subscriptions) > 0 {
		arr := make([]any, len(wc.Subscriptions))
		for i, s := range wc.Subscriptions {
			arr[i] = s
		}
		p["subscriptions"] = arr
	}
	if len(wc.Publish) > 0 {
		arr := make([]any, len(wc.Publish))
		for i, s := range wc.Publish {
			arr[i] = s
		}
		p["publish"] = arr
	}
	if wc.RootDir != "" {
		p["root_dir"] = wc.RootDir
	}
	return p
}

func validateWorkers(cfg *TemplateConfig) (*TemplateConfig, error) {
	for i, w := range cfg.Workers {
		if w.Type == "" {
			return nil, fmt.Errorf("project: worker %d: type is required", i)
		}
		if w.ID == "" {
			return nil, fmt.Errorf("project: worker %d: id is required", i)
		}
	}
	return cfg, nil
}
