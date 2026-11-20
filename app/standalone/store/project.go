package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProjectPorts records the ports assigned to a project instance: its event-bus
// (HTTP transport) port and its WebUI port. Zero means "not allocated".
type ProjectPorts struct {
	Bus   int `json:"bus,omitempty"`
	WebUI int `json:"webui,omitempty"`
}

// Project is the project-level metadata. Worker manifests live in
// workers.json / workers.archived.json, bus identities in identities.json.
type Project struct {
	SchemaVersion int          `json:"schema_version,omitempty"`
	ID            string       `json:"id"`
	CreatedAt     string       `json:"created_at,omitempty"`
	Ports         ProjectPorts `json:"ports,omitempty"`
	UploadDir     string       `json:"upload_dir,omitempty"`
}

// ProjectPath returns the project.json file in dir.
func ProjectPath(dir string) string { return filepath.Join(dir, "project.json") }

// LoadProject reads dir/project.json, running the one-time conversion first.
func LoadProject(dir string) (*Project, error) {
	if err := ensureProject(dir); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(ProjectPath(dir))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("store: project not found in %s", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", ProjectPath(dir), err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", ProjectPath(dir), err)
	}
	return &p, nil
}

// SaveProject writes dir/project.json atomically.
func SaveProject(dir string, p *Project) error {
	if p.SchemaVersion == 0 {
		p.SchemaVersion = SchemaVersion
	}
	if p.ID == "" {
		return fmt.Errorf("store: cannot save project with empty id")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("store: mkdir: %w", err)
	}
	return writeJSONAtomic(ProjectPath(dir), p)
}
