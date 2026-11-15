// Package template owns project templates: the built-in presets, the on-disk
// templates directory they are seeded into, loading/precedence rules, and the
// zip package format a template is shared as.
//
// A template is the seed of a project, not a running thing: it is a list of
// worker declarations plus optional program resources. It therefore depends on
// the worker model (app/project/worker) but on nothing about a live project —
// the "project → template" direction (export) lives in app/project, which is
// the only side that reads a project.
package template

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/niq-run/niq/app/niqhome"
	"github.com/niq-run/niq/app/project/migrate"
	projworker "github.com/niq-run/niq/app/project/worker"
)

// Template is the top-level structure of a template file: the set of workers a
// new project is seeded with.
type Template struct {
	SchemaVersion int                      `json:"schema_version,omitempty"`
	Workers       []projworker.Declaration `json:"workers"`

	// UploadDir is where the WebUI stores files uploaded from the talk input
	// (project.json upload_dir, project metadata). Empty means <project dir>/uploads. Relative
	// paths resolve against the project dir.
	UploadDir string `json:"upload_dir,omitempty"`
}

//go:embed preset
var presetFS embed.FS

// Parse parses and validates a template body: JSON syntax plus the structural
// minimum (at least one worker, each with a type and an id).
func Parse(raw []byte) (*Template, error) {
	var cfg Template
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("project: parse config: %w", err)
	}
	return Validate(&cfg)
}

// Parse is also the control API's validator (POST with a full template, PUT of
// an existing one): the rules are the same as for a template file on disk.
func Validate(cfg *Template) (*Template, error) {
	if cfg.SchemaVersion == 0 {
		cfg.SchemaVersion = 2
	}
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

// ReadPresetFile reads one file of a built-in preset (its programs/ resources,
// for tests and tooling that check what ships in the box).
func ReadPresetFile(path string) ([]byte, error) {
	return presetFS.ReadFile("preset/" + path)
}

// LoadPreset loads a built-in template by name (the directory under preset/).
func LoadPreset(name string) (*Template, error) {
	raw, err := presetFS.ReadFile("preset/" + name + "/template.json")
	if err != nil {
		return nil, fmt.Errorf("project: preset %q not found", name)
	}
	return Parse(raw)
}

// TemplatesDir is the shared "common" templates directory
// (~/.niq/common/templates/<name>/): template.json holds the worker set, an
// optional programs/ subdirectory carries the program resources the workers
// reference. Templates are seeded here from the built-ins on first run and
// become user-editable files from then on.
func TemplatesDir() string {
	return filepath.Join(niqhome.Root(), "common", "templates")
}

// Seed copies the built-in preset templates to dir, but only when dir holds no
// template yet (first-run seeding). Idempotent, best-effort on empty dirs.
func Seed(dir string) error {
	if dir == "" {
		return nil
	}
	if names, _ := ListIn(dir); len(names) > 0 {
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

// CopyPrograms copies a template's programs/ resources into dst. The on-disk
// copy wins when it exists (the user-edited template is authoritative), falling
// back to the embedded built-in before seeding — the same precedence Load
// applies to template.json. An absent programs/ dir is a no-op (false).
func CopyPrograms(dir, name, dst string) (bool, error) {
	if dir != "" {
		src := ProgramsDir(dir, name)
		if _, err := os.Stat(src); err == nil {
			return true, CopyDir(src, dst)
		}
	}
	if name == "" {
		return false, nil
	}
	sub, err := fs.Sub(presetFS, "preset/"+name+"/programs")
	if err != nil {
		return false, nil // the built-in carries no programs/ either
	}
	if _, err := fs.Stat(sub, "."); err != nil {
		return false, nil
	}
	return true, os.CopyFS(dst, sub)
}

// Dir returns the on-disk directory of a template under the templates dir.
func Dir(dir, name string) string {
	return filepath.Join(dir, projworker.SanitizeID(name))
}

// Path returns the path of a template's template.json under the templates dir.
func Path(dir, name string) string {
	return filepath.Join(Dir(dir, name), "template.json")
}

// ProgramsDir returns the path of a template's programs/ resources.
func ProgramsDir(dir, name string) string {
	return filepath.Join(Dir(dir, name), "programs")
}

// ListIn returns the template names found in dir: subdirectories that hold a
// template.json, sorted.
func ListIn(dir string) ([]string, error) {
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

// ReadRaw returns the raw JSON for a template, preferring the on-disk copy over
// the embedded built-in (used to clone templates).
func ReadRaw(dir, name string) ([]byte, error) {
	if dir != "" {
		if b, err := os.ReadFile(Path(dir, name)); err == nil {
			return b, nil
		}
	}
	return presetFS.ReadFile("preset/" + name + "/template.json")
}

// List returns the available project template names, preferring the on-disk
// common/templates dir (seeded + user editable) and falling back to the
// embedded built-ins.
func List() ([]string, error) {
	if err := migrate.EnsureTemplates(TemplatesDir()); err != nil {
		return nil, err
	}
	if names, err := ListIn(TemplatesDir()); err == nil && len(names) > 0 {
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

// Load loads a project template from the on-disk templates dir, preferring the
// disk copy over the embedded built-in. The disk copy is what a user edits; the
// embedded one is the fallback before seeding.
func Load(dir, name string) (*Template, error) {
	if dir != "" {
		if err := migrate.EnsureTemplates(dir); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(Path(dir, name))
		if err == nil {
			return Parse(raw)
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("project: read template %s: %w", name, err)
		}
	}
	return LoadPreset(name)
}
