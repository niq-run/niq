// Migration of the pre-project "global swarm" layout into the per-project one.
//
// Before the multi-project layout, a single swarm kept everything under
// ~/.niq directly: state/workers (managed worker snapshots), id (bus identity
// registry) and programs. The new layout keeps only shared data in ~/.niq/common
// and puts everything else under ~/.niq/projects/<id>/. This migrates that
// legacy single swarm into a project so it can be run with `niq project run`.
package swarm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MigrateLegacyProject wraps the legacy global swarm (state/ id/ programs/) under
// <base> into a project id, generating project.json from the persisted workers.
// It moves (not copies) the legacy dirs. Idempotent: errors if the project
// already exists. base is the niq home dir (e.g. ~/.niq).
func MigrateLegacyProject(id, base string) (string, error) {
	if id == "" {
		id = "default"
	}
	projDir := filepath.Join(base, "projects", sanitizeProjectID(id))
	projPath := filepath.Join(projDir, "project.json")
	if _, err := os.Stat(projPath); err == nil {
		return "", fmt.Errorf("project %q already exists", id)
	}
	if err := os.MkdirAll(projDir, 0755); err != nil {
		return "", fmt.Errorf("migrate: mkdir %s: %w", projDir, err)
	}

	workers := legacyWorkersFromState(filepath.Join(base, "state", "workers"))
	proj := Project{
		ID:        id,
		CreatedAt: time.Now().Format(time.RFC3339),
		Workers:   workers,
	}

	// Move the legacy global dirs into the project. Dirs that are missing or
	// already present are skipped (best-effort).
	for _, c := range []struct{ src, dst string }{
		{filepath.Join(base, "state"), filepath.Join(projDir, "state")},
		{filepath.Join(base, "id"), filepath.Join(projDir, "id")},
		{filepath.Join(base, "programs"), filepath.Join(projDir, "programs")},
	} {
		if _, err := os.Stat(c.src); err != nil {
			continue
		}
		if _, err := os.Stat(c.dst); err == nil {
			continue
		}
		if err := os.Rename(c.src, c.dst); err != nil {
			return "", fmt.Errorf("migrate: move %s -> %s: %w", c.src, c.dst, err)
		}
	}

	if err := saveProjectAt(&proj, projPath); err != nil {
		return projDir, err
	}
	return projDir, nil
}

// legacyWorkersFromState derives a project's managed-worker list from every
// persisted worker config under a legacy state/workers dir.
func legacyWorkersFromState(dir string) []WorkerConfig {
	var out []WorkerConfig
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, de := range entries {
		if !de.IsDir() {
			continue
		}
		var wc struct {
			ID     string         `json:"id"`
			Type   string         `json:"type"`
			Params map[string]any `json:"params"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, de.Name(), "config.json"))
		if err != nil {
			continue
		}
		if err := json.Unmarshal(raw, &wc); err != nil || wc.ID == "" {
			continue
		}
		if wc.Params == nil {
			wc.Params = map[string]any{}
		}
		out = append(out, workerConfigFromParams(wc.ID, wc.Type, wc.Params))
	}
	return out
}

// workerConfigFromParams recomposes a WorkerConfig from a persisted worker's
// params map, keeping only the fields WorkerConfig models. Unknown params (e.g.
// goal/brief) live only in the persisted snapshot and are ignored here.
func workerConfigFromParams(id, typ string, params map[string]any) WorkerConfig {
	wc := WorkerConfig{Type: typ, ID: id}
	if s, ok := params["instruction"].(string); ok {
		wc.Instruction = s
	}
	if s, ok := params["provider"].(string); ok {
		wc.Provider = s
	}
	if s, ok := params["api_key"].(string); ok {
		wc.APIKey = s
	}
	if s, ok := params["base_url"].(string); ok {
		wc.BaseURL = s
	}
	if s, ok := params["model"].(string); ok {
		wc.Model = s
	}
	if s, ok := params["root_dir"].(string); ok {
		wc.RootDir = s
	}
	wc.Subscriptions = stringSliceParam(params["subscriptions"])
	wc.Publish = stringSliceParam(params["publish"])
	return wc
}

func stringSliceParam(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// saveProjectAt writes a project's definition to an explicit path (used by the
// migration which may target a non-default base).
func saveProjectAt(p *Project, path string) error {
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("project: marshal %s: %w", p.ID, err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0644); err != nil {
		return fmt.Errorf("project: write %s: %w", p.ID, err)
	}
	return nil
}