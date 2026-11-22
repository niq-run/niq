package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/app/standalone/contract"
)

// Project is project.json — the only project shape that is persisted. What a
// client sees is contract.View, built by the launcher: a field added here is
// private to this machine until it is also added there.
type Project struct {
	SchemaVersion int            `json:"schema_version,omitempty"`
	ID            string         `json:"id"`
	CreatedAt     string         `json:"created_at,omitempty"`
	Ports         contract.Ports `json:"ports,omitempty"`
	UploadDir     string         `json:"upload_dir,omitempty"`
}

// ProjectFile returns the project.json file in dir.
func ProjectFile(dir string) string { return filepath.Join(dir, "project.json") }

// LoadProject reads dir/project.json, running the one-time conversion first.
func LoadProject(dir string) (*Project, error) {
	if err := ensureProject(dir); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(ProjectFile(dir))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("store: project not found in %s", dir)
	}
	if err != nil {
		return nil, fmt.Errorf("store: read %s: %w", ProjectFile(dir), err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("store: parse %s: %w", ProjectFile(dir), err)
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
	return writeJSONAtomic(ProjectFile(dir), p)
}
