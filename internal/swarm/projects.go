// Project storage: each project lives under ~/.niq/projects/<id>/. project.json
// carries the ports it last ran on and the archived-worker set; the worker
// definitions and lifecycle state live in the workers/ directory
// (workers/<id>/config.json + state.json) — that directory is the source of
// truth for which workers exist.
package swarm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/54c1/niq/core/worker"
)

// ProjectPorts records the ports assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type ProjectPorts struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// ProjectWorker is a project worker declaration. Managed workers keep their
// authoritative config in the worker directory; the entry here is a light
// reference (id + type). Unmanaged workers are external processes swarm
// launches after the bus is up, using Command/Env/Cwd; their bus credential is
// generated on first launch and persisted here.
type ProjectWorker struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Managed       bool              `json:"managed"`
	Credential    string            `json:"credential,omitempty"`
	Command       []string          `json:"command,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
	Subscriptions []string          `json:"subscriptions,omitempty"`
	Publish       []string          `json:"publish,omitempty"`
}

// Project is the on-disk definition for one project: its ports, archived
// worker ids, and the worker declarations. Managed workers' authoritative
// config lives in the workers/ directory; the project.json entry is a light
// reference. Unmanaged workers are declared here with their launch spec.
type Project struct {
	ID        string          `json:"id"`
	CreatedAt string          `json:"created_at,omitempty"`
	Ports     ProjectPorts    `json:"ports,omitempty"`
	Archived  map[string]bool `json:"archived,omitempty"`
	Workers   []ProjectWorker `json:"workers,omitempty"`
}

// ProjectsRoot returns ~/.niq/projects (created lazily on write).
func ProjectsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq", "projects")
}

// ProjectDir returns the directory for a project id.
func ProjectDir(id string) string { return filepath.Join(ProjectsRoot(), sanitizeID(id)) }

// ProjectPath returns the project.json path for a project id.
func ProjectPath(id string) string { return filepath.Join(ProjectDir(id), "project.json") }

// CreateProject creates a project directory + project.json and seeds each
// template worker. Managed workers get an authoritative config.json in
// workers/ (and a light entry in project.json); unmanaged workers are declared
// in project.json with their full launch spec. It fails if a project with the
// same id already exists.
func CreateProject(id string, template *SwarmConfig) (*Project, error) {
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
	var workers []ProjectWorker
	if template != nil {
		for _, wc := range template.Workers {
			if isManagedWorker(wc) {
				if err := seedWorkerConfig(ProjectDir(id), wc); err != nil {
					return nil, err
				}
				workers = append(workers, ProjectWorker{ID: wc.ID, Type: wc.Type, Managed: true})
				continue
			}
			workers = append(workers, ProjectWorker{
				ID:            wc.ID,
				Type:          wc.Type,
				Managed:       false,
				Credential:    wc.Credential,
				Command:       wc.Command,
				Env:           wc.Env,
				Cwd:           wc.Cwd,
				Subscriptions: wc.Subscriptions,
				Publish:       wc.Publish,
			})
		}
	}
	p := &Project{ID: id, CreatedAt: time.Now().Format(time.RFC3339), Workers: workers}
	if err := saveProject(p); err != nil {
		return nil, err
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
func UnmanagedWorkers(p *Project) []ProjectWorker {
	var out []ProjectWorker
	for _, w := range p.Workers {
		if !w.Managed {
			out = append(out, w)
		}
	}
	return out
}

// FindWorker returns a project's worker declaration by id.
func FindWorker(p *Project, id string) (ProjectWorker, bool) {
	for _, w := range p.Workers {
		if w.ID == id {
			return w, true
		}
	}
	return ProjectWorker{}, false
}

// workerConfigPath returns the authoritative config.json path for a worker.
func workerConfigPath(projDir, id string) string {
	return filepath.Join(projDir, "workers", sanitizeID(id), "config.json")
}

// seedWorkerConfig writes a worker's authoritative config.json from its full
// (template) definition. Never overwrites an existing config (config.json wins).
func seedWorkerConfig(projDir string, wc WorkerConfig) error {
	path := workerConfigPath(projDir, wc.ID)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("seed worker config: %w", err)
	}
	cfg := worker.WorkerConfig{ID: wc.ID, Type: wc.Type, Params: workerConfigParams(wc)}
	return writeWorkerConfig(path, cfg)
}

// readWorkerConfig loads a worker's authoritative config.json.
func readWorkerConfig(projDir, id string) (worker.WorkerConfig, bool) {
	raw, err := os.ReadFile(workerConfigPath(projDir, id))
	if err != nil {
		return worker.WorkerConfig{}, false
	}
	var cfg worker.WorkerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return worker.WorkerConfig{}, false
	}
	return cfg, true
}

func writeWorkerConfig(path string, cfg worker.WorkerConfig) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0644)
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
