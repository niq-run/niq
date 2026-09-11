// Project storage: each project lives under <niq root>/projects/<id>/. project.json
// carries the ports it last ran on, the archived-worker set and every worker
// declaration (id/type/config) — it is the source of truth for which workers
// exist. The workers/ directory holds only runtime state: workers/<id>/state.json
// for managed workers, workers/<id>/stdout.log for external ones.
package project

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/niq-run/niq/internal/niqhome"
)

// ProjectPorts records the ports assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type ProjectPorts struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// A worker declaration is the full worker config — the same shape as a
// template's worker entry. project.json is the single source of truth for
// which workers exist and how they are built; a worker's own directory holds
// only its runtime state (and, for external processes, its stdout log).
type Project struct {
	ID        string          `json:"id"`
	CreatedAt string          `json:"created_at,omitempty"`
	Ports     ProjectPorts    `json:"ports,omitempty"`
	Archived  map[string]bool `json:"archived,omitempty"`
	UploadDir string          `json:"upload_dir,omitempty"`
	Workers   []WorkerConfig  `json:"workers,omitempty"`
}

// ProjectsRoot returns <niq root>/projects (created lazily on write).
func ProjectsRoot() string {
	return filepath.Join(niqhome.Root(), "projects")
}

// ProjectDir returns the directory for a project id.
func ProjectDir(id string) string { return filepath.Join(ProjectsRoot(), sanitizeID(id)) }

// ProjectPath returns the project.json path for a project id.
func ProjectPath(id string) string { return filepath.Join(ProjectDir(id), "project.json") }

// CreateProject creates a project directory + project.json and seeds each
// template worker declaration (the {project} placeholder in mount paths is
// resolved against the new project id). templateName names the source template
// in the templates dir: its programs/ resources, if any, are copied into the
// new project's programs/. It fails if a project with the same id exists.
func CreateProject(id, templateName string, template *TemplateConfig) (*Project, error) {
	if id == "" {
		return nil, fmt.Errorf("project: id is required")
	}
	path := ProjectPath(id)
	if _, err := os.Lstat(path); err == nil {
		return nil, fmt.Errorf("project: %q already exists", id)
	}
	if err := os.MkdirAll(ProjectDir(id), 0755); err != nil {
		return nil, fmt.Errorf("project: mkdir: %w", err)
	}
	var workers []WorkerConfig
	if template != nil {
		for _, wc := range template.Workers {
			workers = append(workers, instantiateWorker(wc, id))
		}
	}
	uploadDir := ""
	if template != nil {
		uploadDir = template.UploadDir
	}
	p := &Project{ID: id, CreatedAt: time.Now().Format(time.RFC3339), UploadDir: uploadDir, Workers: workers}
	if err := saveProject(p); err != nil {
		return nil, err
	}
	if templateName != "" {
		src := TemplateProgramsDir(TemplatesDir(), templateName)
		if _, err := os.Stat(src); err == nil {
			if err := CopyDir(src, ProjectProgramsDir(id)); err != nil {
				return nil, fmt.Errorf("project: copy template programs: %w", err)
			}
		}
	}
	return p, nil
}

// isManagedWorker reports whether a template worker is host-managed. The
// managed field defaults to true (nil → managed); only an explicit false
// marks a worker as an external process.
func isManagedWorker(wc WorkerConfig) bool {
	return wc.Managed == nil || *wc.Managed
}

// UnmanagedWorkers returns the external-process worker declarations.
func UnmanagedWorkers(p *Project) []WorkerConfig {
	var out []WorkerConfig
	for _, w := range p.Workers {
		if !isManagedWorker(w) {
			out = append(out, w)
		}
	}
	return out
}

// FindWorker returns a project's worker declaration by id.
func FindWorker(p *Project, id string) (WorkerConfig, bool) {
	for _, w := range p.Workers {
		if w.ID == id {
			return w, true
		}
	}
	return WorkerConfig{}, false
}

// AppendWorkerDecl adds a worker declaration to a project's project.json
// (persisted immediately) — how a worker spawned at runtime joins the project
// and survives a restart. An id that is already declared is left untouched:
// runtime never overwrites an existing config, it only fills in the gaps.
func AppendWorkerDecl(projectID string, wc WorkerConfig) error {
	p, err := LoadProject(projectID)
	if err != nil {
		return err
	}
	if _, ok := FindWorker(p, wc.ID); ok {
		return nil
	}
	p.Workers = append(p.Workers, wc)
	return SaveProject(p)
}

// UpdateWorkerMeta updates a declared worker's display metadata (tags /
// description) in project.json. These are WebUI-management fields, not runtime
// state: the running worker ignores them, so no live worker operation is
// needed — this is a pure declaration edit. The caller (WebUI server) also
// refreshes its in-memory workerMeta so the change shows without a restart.
// The worker must already be declared.
func UpdateWorkerMeta(projectID, id string, tags []string, desc string) error {
	probe := WorkerConfig{Tags: tags, Description: desc}
	if err := validateWorkerMeta(probe); err != nil {
		return err
	}
	p, err := LoadProject(projectID)
	if err != nil {
		return err
	}
	idx := -1
	for i, w := range p.Workers {
		if w.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("worker %s not found", id)
	}
	p.Workers[idx].Tags = append([]string(nil), tags...)
	p.Workers[idx].Description = desc
	return SaveProject(p)
}

// RemoveWorkerDecl removes a worker's declaration from a project's project.json
// (persisted immediately). It only edits the declaration; it does not stop a
// running process or touch the bus registry / state dirs.
func RemoveWorkerDecl(projectID, id string) error {
	p, err := LoadProject(projectID)
	if err != nil {
		return err
	}
	out := p.Workers[:0]
	for _, w := range p.Workers {
		if w.ID != id {
			out = append(out, w)
		}
	}
	p.Workers = out
	return SaveProject(p)
}

// removeWorkerStateDir deletes a worker's persisted per-worker directory
// (<project>/workers/<id>), i.e. its config/state/snapshot/stdout. Missing
// dirs are a no-op.
func removeWorkerStateDir(projectID, id string) error {
	dir := filepath.Join(workersRoot(projectID), sanitizeID(id))
	return os.RemoveAll(dir)
}

// workersRoot returns the project's per-worker state directory root. The
// authoritative layout lives in run Assembly's StateDir; this mirrors it so
// deletes can clean up persisted state consistently.
func workersRoot(projectID string) string {
	return filepath.Join(ProjectDir(projectID), "workers")
}

// instantiateWorker resolves template-layer placeholders in a worker
// declaration for a concrete project: "{project}" in mount paths becomes the
// project id. Called once when a declaration is instantiated into a project
// (template seeding, WebUI creation) — project.json carries real paths and the
// runtime never sees the placeholder.
func instantiateWorker(wc WorkerConfig, projectID string) WorkerConfig {
	if len(wc.Mounts) == 0 {
		return wc
	}
	mounts := make([]string, len(wc.Mounts))
	for i, m := range wc.Mounts {
		mounts[i] = strings.ReplaceAll(m, "{project}", projectID)
	}
	wc.Mounts = mounts
	return wc
}

// LoadProject loads a project's definition from its project.json.
func LoadProject(id string) (*Project, error) {
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

// SaveProject writes a project's definition back to project.json.
func SaveProject(p *Project) error {
	return saveProject(p)
}

func saveProject(p *Project) error {
	if p.ID == "" {
		return fmt.Errorf("project: cannot save project with empty id")
	}
	if err := os.MkdirAll(ProjectDir(p.ID), 0755); err != nil {
		return fmt.Errorf("project: mkdir: %w", err)
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("project: marshal %s: %w", p.ID, err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(ProjectPath(p.ID), raw, 0644); err != nil {
		return fmt.Errorf("project: write %s: %w", p.ID, err)
	}
	return nil
}

// ListProjects returns every project's definition, sorted by id.
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
		if !de.IsDir() {
			continue
		}
		p, err := LoadProject(de.Name())
		if err != nil {
			continue // skip unreadable / half-created projects
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// projectArchiver implements the webui.ArchivedStore backed by a project's
// project.json archived map, so archived-worker state persists and the project
// WebUI can read/toggle it.
type projectArchiver struct {
	id string
}

func (a projectArchiver) Archived() []string {
	p, err := LoadProject(a.id)
	if err != nil {
		return nil
	}
	out := []string{}
	for id, v := range p.Archived {
		if v {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (a projectArchiver) SetArchived(id string, v bool) error {
	p, err := LoadProject(a.id)
	if err != nil {
		return err
	}
	if p.Archived == nil {
		p.Archived = map[string]bool{}
	}
	if v {
		p.Archived[id] = true
	} else {
		delete(p.Archived, id)
	}
	return SaveProject(p)
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
