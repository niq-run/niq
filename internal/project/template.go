// Template loading: the embedded presets (preset/), seeding the on-disk
// templates dir, and the loader that prefers a user-edited disk template over
// the embedded built-in. The template data model itself lives in types.go;
// the config↔params roundtrip lives in params.go.
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

	"github.com/niq-run/niq/internal/niqhome"
)

//go:embed preset
var presetFS embed.FS

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
	return filepath.Join(niqhome.Root(), "common", "templates")
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
