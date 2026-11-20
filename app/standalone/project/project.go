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
// The concrete store implementations live in ../store; this package owns the
// project's lifecycle (create / list / link) and the runtime entry.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	projtemplate "github.com/niq-run/niq/app/standalone/project/template"
	"github.com/niq-run/niq/app/standalone/store"
	"github.com/niq-run/niq/app/standalone/store/migrate"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// SchemaVersion is the on-disk format this package reads and writes. Legacy
// projects are converted once by store/migrate before they reach here.
const SchemaVersion = store.SchemaVersion

// ProjectPorts and Project are the on-disk metadata shape; the IO lives in
// store/project.go. The aliases keep the project package's vocabulary.
type (
	ProjectPorts = store.ProjectPorts
	Project      = store.Project
)

// CreateProject creates a project directory, its metadata file and its active
// worker file, seeding the template's declarations. It fails if the project id
// already exists.
func CreateProject(id, templateName string, template *TemplateConfig) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project: id is required")
	}
	if _, err := os.Lstat(store.ProjectPath(id)); err == nil {
		return nil, fmt.Errorf("project: %q already exists", id)
	}
	if err := os.MkdirAll(store.ProjectDir(id), 0755); err != nil {
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
	if err := store.SaveWorkers(store.ProjectDir(id), set); err != nil {
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
	p, err := store.LoadProject(store.ProjectDir(id))
	if err != nil {
		return nil, fmt.Errorf("project: %q: %w", id, err)
	}
	return p, nil
}

// SaveProject writes a project's metadata back to project.json.
func SaveProject(p *Project) error {
	return store.SaveProject(store.ProjectDir(p.ID), p)
}

// ListProjects returns every project's metadata, sorted by id.
func ListProjects() ([]Project, error) {
	entries, err := os.ReadDir(store.ProjectsRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("project: read root: %w", err)
	}
	var out []Project
	for _, de := range entries {
		fi, err := os.Stat(filepath.Join(store.ProjectsRoot(), de.Name()))
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

	id := store.SanitizeID(filepath.Base(abs))
	if raw, err := os.ReadFile(filepath.Join(abs, "project.json")); err == nil {
		var p Project
		if json.Unmarshal(raw, &p) == nil && p.ID != "" {
			id = store.SanitizeID(p.ID)
		}
	}
	link := store.ProjectDir(id)
	if _, err := os.Lstat(link); err == nil {
		return nil, fmt.Errorf("project: %q already exists at %s", id, link)
	}
	if err := os.MkdirAll(store.ProjectsRoot(), 0755); err != nil {
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
		return store.SaveProject(dir, &Project{SchemaVersion: SchemaVersion, ID: id, CreatedAt: time.Now().Format(time.RFC3339)})
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
	return store.SaveProject(dir, &p)
}
