// Project metadata and lifecycle: each project lives under
// <niq root>/projects/<id>/.
//
//	project.json            project metadata only (id / ports / created_at / upload_dir)
//	workers.json            active worker manifests
//	workers.archived.json   archived worker manifests (same shape)
//	identities.json         bus identities
//	root_secret             bus token signing secret
//	workers/<id>/           runtime state (state.json / stdout.log / process state)
//
// The concrete store implementations live in ./store; this package owns the
// project's identity (id → directory) and the creation/link lifecycle.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/niq-run/niq/app/standalone/home"
	"github.com/niq-run/niq/app/standalone/project/migrate"
	"github.com/niq-run/niq/app/standalone/project/store"
	projtemplate "github.com/niq-run/niq/app/standalone/project/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// SchemaVersion is the on-disk format this package reads and writes. Legacy
// projects are converted once by app/project/migrate before they reach here.
const SchemaVersion = store.SchemaVersion

// ProjectPorts records the ports assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type ProjectPorts struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// Project is the project-level metadata. Worker declarations live in
// workers.json / workers.archived.json, never here.
type Project struct {
	SchemaVersion int          `json:"schema_version,omitempty"`
	ID            string       `json:"id"`
	CreatedAt     string       `json:"created_at,omitempty"`
	Ports         ProjectPorts `json:"ports,omitempty"`
	UploadDir     string       `json:"upload_dir,omitempty"`
}

// ProjectsRoot returns <niq root>/projects (created lazily on write).
func ProjectsRoot() string { return filepath.Join(home.Root(), "projects") }

// ProjectDir returns the directory for a project id.
func ProjectDir(id string) string { return filepath.Join(ProjectsRoot(), sanitizeID(id)) }

// ProjectPath returns the project.json path for a project id.
func ProjectPath(id string) string { return filepath.Join(ProjectDir(id), "project.json") }

// RootSecretPath returns the project's token-signing secret path.
func RootSecretPath(id string) string { return filepath.Join(ProjectDir(id), "root_secret") }

// LogPath is where a project process writes its log. The date in the filename
// gives free daily rotation: a new day starts a new file, same-day restarts
// append.
func LogPath(id string) string {
	return filepath.Join(ProjectDir(id), "logs", "niq-"+time.Now().Format("2006-01-02")+".log")
}

// CreateProject creates a project directory, its metadata file and its active
// worker file, seeding the template's declarations. It fails if the project id
// already exists.
func CreateProject(id, templateName string, template *TemplateConfig) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project: id is required")
	}
	if _, err := os.Lstat(ProjectPath(id)); err == nil {
		return nil, fmt.Errorf("project: %q already exists", id)
	}
	if err := os.MkdirAll(ProjectDir(id), 0755); err != nil {
		return nil, fmt.Errorf("project: mkdir: %w", err)
	}
	p := &Project{SchemaVersion: SchemaVersion, ID: id, CreatedAt: time.Now().Format(time.RFC3339)}
	if template != nil {
		p.UploadDir = template.UploadDir
	}
	if err := SaveProject(p); err != nil {
		return nil, err
	}
	set := &store.WorkerSet{SchemaVersion: SchemaVersion}
	if template != nil {
		for _, d := range template.Workers {
			set.Workers = append(set.Workers, projworker.Instantiate(d, id))
		}
	}
	if err := store.SaveWorkers(ProjectDir(id), set); err != nil {
		return nil, err
	}
	if templateName != "" {
		if _, err := projtemplate.CopyPrograms(projtemplate.TemplatesDir(), templateName, ProjectProgramsDir(id)); err != nil {
			return nil, fmt.Errorf("project: copy template programs: %w", err)
		}
	}
	return p, nil
}

// LoadProject loads a project's metadata from project.json.
func LoadProject(id string) (*Project, error) {
	// One-time conversion of legacy projects. Idempotent and cheap once the
	// schema marker is present.
	if err := migrate.EnsureProject(ProjectDir(id)); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(ProjectPath(id))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("project: %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("project: read %s: %w", id, err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("project: parse %s: %w", id, err)
	}
	return &p, nil
}

// SaveProject writes a project's metadata back to project.json.
func SaveProject(p *Project) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	if p.ID == "" {
		return fmt.Errorf("project: cannot save project with empty id")
	}
	if err := os.MkdirAll(ProjectDir(p.ID), 0755); err != nil {
		return fmt.Errorf("project: mkdir: %w", err)
	}
	return writeJSONAtomic(ProjectPath(p.ID), p)
}

// writeJSONAtomic writes v as JSON through a temp file + rename, so a crash
// never leaves a half-written declaration file.
func writeJSONAtomic(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("project: marshal %s: %w", path, err)
	}
	raw = append(raw, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("project: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("project: rename %s: %w", path, err)
	}
	return nil
}

// writeProjectJSON is kept for the linker: it seeds project.json into an
// external directory.
func writeProjectJSON(path string, p *Project) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	return writeJSONAtomic(path, p)
}

// ListProjects returns every project's metadata, sorted by id.
func ListProjects() ([]Project, error) {
	entries, err := os.ReadDir(ProjectsRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("project: read root: %w", err)
	}
	var out []Project
	for _, de := range entries {
		fi, err := os.Stat(filepath.Join(ProjectsRoot(), de.Name()))
		if err != nil || !fi.IsDir() {
			continue
		}
		p, err := LoadProject(de.Name())
		if err != nil {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// LinkProject registers an external directory as a niq project by symlinking it
// into <niq root>/projects/<id>.
func LinkProject(dir string) (*Project, error) {
	if dir == "" {
		return nil, fmt.Errorf("project: directory is required")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("project: resolve %q: %w", dir, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("project: %q: %w", dir, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("project: %q is not a directory", dir)
	}

	id := sanitizeID(filepath.Base(abs))
	if raw, err := os.ReadFile(filepath.Join(abs, "project.json")); err == nil {
		var p Project
		if json.Unmarshal(raw, &p) == nil && p.ID != "" {
			id = sanitizeID(p.ID)
		}
	}
	link := ProjectDir(id)
	if _, err := os.Lstat(link); err == nil {
		return nil, fmt.Errorf("project: %q already exists at %s", id, link)
	}
	if err := os.MkdirAll(ProjectsRoot(), 0755); err != nil {
		return nil, fmt.Errorf("project: mkdir: %w", err)
	}
	if err := os.Symlink(filepath.Clean(abs), link); err != nil {
		return nil, fmt.Errorf("project: link %s -> %s: %w", link, abs, err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(link)
		}
	}()
	if err := ensureProjectJSON(abs, id); err != nil {
		return nil, err
	}
	ok = true
	return LoadProject(id)
}

// ensureProjectJSON makes sure a linked directory carries project.json metadata,
// creating it when absent or backfilling the id when present. Worker files are
// left alone: they are the project's source of truth.
func ensureProjectJSON(dir, id string) error {
	// A linked directory may be a legacy niq project: convert it BEFORE reading
	// project.json into the metadata struct, or the legacy workers array would
	// be dropped on the rewrite.
	if err := migrate.EnsureProject(dir); err != nil {
		return err
	}
	path := filepath.Join(dir, "project.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeProjectJSON(path, &Project{SchemaVersion: SchemaVersion, ID: id, CreatedAt: time.Now().Format(time.RFC3339)})
	}
	if err != nil {
		return fmt.Errorf("project: read %s: %w", path, err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("project: parse %s: %w", path, err)
	}
	p.ID = id
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	return writeProjectJSON(path, &p)
}

func sanitizeID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
