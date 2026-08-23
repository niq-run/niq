// Project storage: each project lives under ~/.niq/projects/<id>/ with a
// project.json carrying the full startup definition (the workers to launch)
// and the ports it last ran on. project.json is generated from a template on
// project creation and is the authoritative, continuously-edited definition
// afterwards — it has no ongoing relationship with the template.
package swarm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ProjectPorts records the ports assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type ProjectPorts struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// Project is the on-disk startup definition for one project.
type Project struct {
	ID        string          `json:"id"`
	CreatedAt string          `json:"created_at,omitempty"`
	Ports     ProjectPorts    `json:"ports,omitempty"`
	Workers   []WorkerConfig  `json:"workers,omitempty"` // managed workers to launch
}

// ProjectsRoot returns ~/.niq/projects (created lazily on write).
func ProjectsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niq", "projects")
}

// ProjectDir returns the directory for a project id.
func ProjectDir(id string) string { return filepath.Join(ProjectsRoot(), sanitizeProjectID(id)) }

// ProjectPath returns the project.json path for a project id.
func ProjectPath(id string) string { return filepath.Join(ProjectDir(id), "project.json") }

// CreateProject creates a project directory + project.json from a template's
// worker definitions. It fails if a project with the same id already exists.
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
	var workers []WorkerConfig
	if template != nil {
		workers = template.Workers
	}
	p := &Project{ID: id, CreatedAt: time.Now().Format(time.RFC3339), Workers: workers}
	if err := saveProject(p); err != nil {
		return nil, err
	}
	return p, nil
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
	// deterministic order by id
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].ID < out[j-1].ID; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// sanitizeProjectID keeps project ids filesystem-safe for the per-project dir.
func sanitizeProjectID(id string) string {
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