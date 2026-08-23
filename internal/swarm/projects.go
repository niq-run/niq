// Project storage: each project lives under ~/.niq/projects/<id>/ with a
// project.json carrying the full startup definition (the workers to launch)
// and the ports it last ran on. project.json is generated from a template on
// project creation and is the authoritative, continuously-edited definition
// afterwards — it has no ongoing relationship with the template.
package swarm

import (
	"encoding/json"
	"fmt"
	"log"
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
	ID        string         `json:"id"`
	CreatedAt string         `json:"created_at,omitempty"`
	Ports     ProjectPorts   `json:"ports,omitempty"`
	Workers   []WorkerConfig `json:"workers,omitempty"` // managed workers to launch
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

// MigrateProjectLayout moves a project into the current layout and consolidates
// the sqlite event files under an event/ dir. Targets:
//
//	projects/<id>/ { project.json, id/, programs/, workers/, events/events.db[+-wal,-shm] }
//
// It handles both prior layouts: the nested state/ dir (state/workers, state/events.db)
// and the intermediate flat one (workers/, events.db at the project root).
// Idempotent and best-effort per file so a partial move never loses data.
func MigrateProjectLayout(id string) error {
	projDir := ProjectDir(id)
	eventsDir := filepath.Join(projDir, "events")

	// A previously-created singular event/ dir → events/.
	singularEvent := filepath.Join(projDir, "event")
	if _, err := os.Stat(singularEvent); err == nil {
		if _, err := os.Stat(eventsDir); os.IsNotExist(err) {
			if err := os.Rename(singularEvent, eventsDir); err != nil {
				log.Printf("[migrate] event -> events: %v", err)
			}
		}
	}

	// 1) state/ -> workers/ (nested layout)
	stateDir := filepath.Join(projDir, "state")
	if _, err := os.Stat(stateDir); err == nil {
		oldWorkers := filepath.Join(stateDir, "workers")
		newWorkers := filepath.Join(projDir, "workers")
		if _, err := os.Stat(oldWorkers); err == nil {
			if _, err := os.Stat(newWorkers); os.IsNotExist(err) {
				if err := os.Rename(oldWorkers, newWorkers); err != nil {
					log.Printf("[migrate] move workers: %v", err)
				}
			} else {
				// workers/ already exists: the old state/workers is a stale leftover
				// (no config, old snapshots). Drop it — merging could clobber the
				// current worker state with old data.
				_ = os.RemoveAll(oldWorkers)
			}
		}
		// state/events.db* -> event/
		moveSQLiteFiles(stateDir, eventsDir)
		// Drop the now-empty state dir.
		if ents, err := os.ReadDir(stateDir); err == nil && len(ents) == 0 {
			_ = os.Remove(stateDir)
		}
	}

	// 2) events.db* at the project root -> event/ (intermediate flat layout)
	moveSQLiteFiles(projDir, eventsDir)

	return nil
}

// moveSQLiteFiles moves events.db and its WAL/SHM sidecars from src into dst,
// creating dst and skipping any file that already exists at dst.
func moveSQLiteFiles(src, dst string) {
	if _, err := os.Stat(src); err != nil {
		return
	}
	_ = os.MkdirAll(dst, 0755)
	for _, suffix := range []string{"events.db", "events.db-wal", "events.db-shm"} {
		from := filepath.Join(src, suffix)
		to := filepath.Join(dst, suffix)
		if _, err := os.Stat(from); os.IsNotExist(err) {
			continue
		}
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if err := os.Rename(from, to); err != nil {
			log.Printf("[migrate] move %s -> %s: %v", suffix, dst, err)
		}
	}
}

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

// projectArchiver implements the webui.ArchivedStore backed by a project's
// project.json worker definitions, so archived-worker state persists and the
// project WebUI can read/toggle it.
type projectArchiver struct {
	id string
}

func (a projectArchiver) Archived() []string {
	p, err := LoadProject(a.id)
	if err != nil {
		return nil
	}
	out := []string{}
	for _, w := range p.Workers {
		if w.Archived {
			out = append(out, w.ID)
		}
	}
	return out
}

func (a projectArchiver) SetArchived(id string, v bool) error {
	p, err := LoadProject(a.id)
	if err != nil {
		return err
	}
	found := false
	for i := range p.Workers {
		if p.Workers[i].ID == id {
			p.Workers[i].Archived = v
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("worker %q not found in project %q", id, a.id)
	}
	return SaveProject(p)
}

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
