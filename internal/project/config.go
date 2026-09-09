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
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
)

//go:embed preset
var presetFS embed.FS

// TemplateConfig is the top-level structure of a project template file: the
// set of workers a new project is seeded with.
type TemplateConfig struct {
	Workers []WorkerConfig `json:"workers"`

	// UploadDir is where the WebUI stores files uploaded from the talk input
	// (project.json upload_dir). Empty means <project dir>/uploads. Relative
	// paths resolve against the project dir.
	UploadDir string `json:"upload_dir,omitempty"`
}

// SubscriptionSpec is one SubscribeAllow entry. It accepts either a bare
// event-type string ("worker.discover") or an object restricting the source:
// {"type": "request.completed", "source": "timer"}. The source field limits
// broadcast delivery to events published by that worker; empty means any
// source. It is the config-level spelling of event.EventPattern.
type SubscriptionSpec struct {
	Type   event.EventType `json:"type"`
	Source string          `json:"source,omitempty"`
}

// ToPattern converts the spec into the bus-level EventPattern it stands for.
func (s SubscriptionSpec) ToPattern() event.EventPattern {
	return event.EventPattern{Type: s.Type, SourceID: s.Source}
}

// UnmarshalJSON accepts a bare type string or a {"type","source"} object, so
// existing string-only configs keep parsing.
func (s *SubscriptionSpec) UnmarshalJSON(b []byte) error {
	var typeOnly string
	if err := json.Unmarshal(b, &typeOnly); err == nil {
		s.Type = event.EventType(typeOnly)
		s.Source = ""
		return nil
	}
	type alias SubscriptionSpec
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("project: bad subscription entry %s: %w", b, err)
	}
	*s = SubscriptionSpec(a)
	return nil
}

// PublishSpec is one PublishAllow entry. It accepts either a bare event-type
// string ("request.completed") or an object adding a target restriction:
// {"type": "request.completed", "target": "ws-1"}. The target field limits
// directed sends to that target worker; use "*" (or a bare type string) for
// "any target", which also permits broadcasting the type. It is the
// config-level spelling of event.PublishPattern.
type PublishSpec struct {
	Type   event.EventType `json:"type"`
	Target string          `json:"target,omitempty"`
}

// ToPublishPattern converts the spec into the bus-level PublishPattern it
// stands for.
func (p PublishSpec) ToPublishPattern() event.PublishPattern {
	return event.PublishPattern{Type: p.Type, Target: p.Target}
}

// UnmarshalJSON accepts a bare type string or a {"type","target"} object, so
// existing string-only configs keep parsing. A bare string is an unrestricted
// grant (target "*").
func (p *PublishSpec) UnmarshalJSON(b []byte) error {
	var typeOnly string
	if err := json.Unmarshal(b, &typeOnly); err == nil {
		p.Type = event.EventType(typeOnly)
		p.Target = "*"
		return nil
	}
	type alias PublishSpec
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("project: bad publish entry %s: %w", b, err)
	}
	*p = PublishSpec(a)
	return nil
}

// WorkerConfig describes a single worker instance declaration.
type WorkerConfig struct {
	Type          string             `json:"type"` // reason / workspace / host / timer / hiw / program
	ID            string             `json:"id"`
	Instruction   string             `json:"instruction,omitempty"`
	Provider      string             `json:"provider,omitempty"`
	APIKey        string             `json:"api_key,omitempty"`
	BaseURL       string             `json:"base_url,omitempty"`
	Model         string             `json:"model,omitempty"`
	Subscriptions []SubscriptionSpec `json:"subscriptions,omitempty"`
	Publish       []PublishSpec      `json:"publish,omitempty"`
	// Mounts are the directories a workspace/program worker mounts. The
	// first mount is the primary one (relative paths resolve against it).
	Mounts []string `json:"mounts,omitempty"`
	// Approver is the worker boundary-expansion approval requests go to
	// (workspace workers; default webui-hiw, empty disables the flow).
	Approver string `json:"approver,omitempty"`
	Archived bool   `json:"archived,omitempty"`
	// Managed marks the worker as host-managed (in-process, worker dir is the
	// config authority). nil or true = managed; false = an external process
	// launched by the project via Command/Env/Cwd.
	Managed    *bool             `json:"managed,omitempty"`
	Credential string            `json:"credential,omitempty"`
	Command    []string          `json:"command,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	// Params holds the raw type-specific construction params — the wire
	// format every builder reads and the general escape hatch: spawn-time
	// extras (goal/brief/programs/context tuning) and third-party worker
	// types' private keys have no typed field. It overlays the typed fields
	// above: on the same key, Params wins (see workerParams).
	Params map[string]any `json:"params,omitempty"`
	// Programs holds a reason worker's spawn-time program seeds (inline
	// "skills" the worker loads at start). Only carried when an export is
	// asked to include programs.
	Programs []ProgramSpec `json:"programs,omitempty"`
}

// ProgramSpec is one entry of a reason worker's programs param: an inline
// program seed with its content. It is the template-level spelling of the
// params entry build.go's parsePrograms reads.
type ProgramSpec struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"` // instruction | playbook
	Description string `json:"description,omitempty"`
	Content     string `json:"content,omitempty"`
}

// LoadPreset loads a built-in template by name (the directory under preset/).
func LoadPreset(name string) (*TemplateConfig, error) {
	raw, err := presetFS.ReadFile("preset/" + name + "/template.json")
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

// ValidateTemplate parses and validates a template body arriving over the
// control API (POST with a full template, PUT of an existing one). The same
// rules as template files on disk: workers need a type and an id.
func ValidateTemplate(raw []byte) (*TemplateConfig, error) {
	return parseConfig(raw)
}

// A template is a directory under the shared "common" layer
// (~/.niq/common/templates/<name>/): template.json holds the worker set, an
// optional programs/ subdirectory carries the program resources the workers
// reference. Templates are seeded here from the built-ins on first run and
// become user-editable files from then on.
func TemplatesDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq", "common", "templates")
}

// SeedTemplates copies the built-in preset templates to dir, but only when dir
// holds no template yet (first-run seeding). Idempotent, best-effort on empty
// dirs.
func SeedTemplates(dir string) error {
	if dir == "" {
		return nil
	}
	if names, _ := ListTemplatesIn(dir); len(names) > 0 {
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
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		sub, err := fs.Sub(presetFS, "preset/"+name)
		if err != nil {
			continue
		}
		if err := os.CopyFS(filepath.Join(dir, name), sub); err != nil {
			log.Printf("[project] seed template %s: %v", name, err)
		}
	}
	return nil
}

// TemplateDir returns the on-disk directory of a template under the templates
// dir.
func TemplateDir(dir, name string) string {
	return filepath.Join(dir, sanitizeID(name))
}

// TemplatePath returns the path of a template's template.json under the
// templates dir.
func TemplatePath(dir, name string) string {
	return filepath.Join(TemplateDir(dir, name), "template.json")
}

// TemplateProgramsDir returns the path of a template's programs/ resources.
func TemplateProgramsDir(dir, name string) string {
	return filepath.Join(TemplateDir(dir, name), "programs")
}

// ListTemplatesIn returns the template names found in dir: subdirectories that
// hold a template.json, sorted.
func ListTemplatesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "template.json")); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// CopyDir recursively copies src to dst (dst must not exist). Used to clone
// templates — template.json and programs/ travel together.
func CopyDir(src, dst string) error {
	return os.CopyFS(dst, os.DirFS(src))
}

// ReadTemplateRaw returns the raw JSON for a template, preferring the on-disk
// copy over the embedded built-in (used to clone templates).
func ReadTemplateRaw(dir, name string) ([]byte, error) {
	if dir != "" {
		if b, err := os.ReadFile(TemplatePath(dir, name)); err == nil {
			return b, nil
		}
	}
	return presetFS.ReadFile("preset/" + name + "/template.json")
}

// ListTemplates returns the available project template names, preferring the
// on-disk common/templates dir (seeded + user editable) and falling back to
// the embedded built-ins.
func ListTemplates() ([]string, error) {
	if names, err := ListTemplatesIn(TemplatesDir()); err == nil && len(names) > 0 {
		return names, nil
	}
	entries, err := presetFS.ReadDir("preset")
	if err != nil {
		return nil, fmt.Errorf("project: read embedded templates: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
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
			m := map[string]any{"type": string(s.Type)}
			if s.Source != "" {
				m["source"] = s.Source
			}
			arr[i] = m
		}
		p["subscriptions"] = arr
	}
	if len(wc.Publish) > 0 {
		arr := make([]any, len(wc.Publish))
		for i, s := range wc.Publish {
			m := map[string]any{"type": string(s.Type)}
			if s.Target != "" {
				m["target"] = s.Target
			}
			arr[i] = m
		}
		p["publish"] = arr
	}
	if len(wc.Mounts) > 0 {
		p["mounts"] = wc.Mounts
	}
	if wc.Approver != "" {
		p["approver"] = wc.Approver
	}
	if len(wc.Programs) > 0 {
		arr := make([]any, len(wc.Programs))
		for i, prog := range wc.Programs {
			m := map[string]any{"name": prog.Name, "content_type": prog.ContentType}
			if prog.Description != "" {
				m["description"] = prog.Description
			}
			if prog.Content != "" {
				m["content"] = prog.Content
			}
			arr[i] = m
		}
		p["programs"] = arr
	}
	return p
}

// workerParams returns the params map a worker is actually built from: the
// typed fields lowered to params, overlaid with Params. The typed fields are
// the authored view (template, WebUI form); Params is what the builders read
// and the only place arbitrary per-type keys can live — so it wins.
func workerParams(wc WorkerConfig) map[string]any {
	p := workerConfigParams(wc)
	for k, v := range wc.Params {
		p[k] = v
	}
	return p
}

// spawnConfig lowers a declared worker into the workerhost-level config.
func spawnConfig(wc WorkerConfig) worker.WorkerConfig {
	return worker.WorkerConfig{ID: wc.ID, Type: wc.Type, Params: workerParams(wc)}
}

// declFromSpawn turns a runtime-spawned worker's config into a project.json
// declaration. The params map is stored verbatim — it is the only place
// arbitrary per-type keys (spawn-time goal/brief/programs, third-party worker
// types) can live; the typed fields stay empty.
func declFromSpawn(cfg worker.WorkerConfig) WorkerConfig {
	return WorkerConfig{Type: cfg.Type, ID: cfg.ID, Params: cfg.Params}
}

func validateWorkers(cfg *TemplateConfig) (*TemplateConfig, error) {
	if len(cfg.Workers) == 0 {
		return nil, fmt.Errorf("project: template needs at least one worker")
	}
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
