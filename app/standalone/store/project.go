package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/app/standalone/contract"
)

// The project shapes are shared with every client, so they live in contract;
// this package owns only how project.json is read and written.
type (
	ProjectPorts = contract.Ports
	Project      = contract.Project
)

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
