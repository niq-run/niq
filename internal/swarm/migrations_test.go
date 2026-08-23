package swarm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrateLegacyProject verifies the legacy global swarm (state/id/programs)
// is moved into a project and a project.json is generated from the persisted
// worker configs, all under an injected base.
func TestMigrateLegacyProject(t *testing.T) {
	base := t.TempDir() // pretend this is ~/.niq

	// Legacy managed workers with both config and a snapshot.
	for _, w := range []struct{ id, typ string }{
		{"niq", "reason"}, {"host", "host"},
	} {
		d := filepath.Join(base, "state", "workers", w.id)
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
		cfg := `{"id":"` + w.id + `","type":"` + w.typ + `","params":{"provider":"deepseek","model":"deepseek-v4-flash"}}`
		if err := os.WriteFile(filepath.Join(d, "config.json"), []byte(cfg), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "state.json"), []byte(`{"state":"running","snapshot":"AQM="}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Legacy id + programs dirs.
	os.MkdirAll(filepath.Join(base, "id"), 0755)
	os.WriteFile(filepath.Join(base, "id", "identities.json"), []byte("{}"), 0644)
	os.MkdirAll(filepath.Join(base, "programs"), 0755)

	dir, err := MigrateLegacyProject("default", base)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !strings.HasSuffix(dir, filepath.Join("projects", "default")) {
		t.Fatalf("dir = %q, want .../projects/default", dir)
	}

	// Legacy dirs moved (no longer at base/state, base/id, base/programs).
	for _, p := range []string{"state", "id", "programs"} {
		if _, err := os.Stat(filepath.Join(base, p)); !os.IsNotExist(err) {
			t.Fatalf("legacy %s should be moved", p)
		}
	}
	// Worker state now lives under the project.
	if _, err := os.Stat(filepath.Join(dir, "state", "workers", "niq", "state.json")); err != nil {
		t.Fatalf("worker state not under project: %v", err)
	}

	// project.json carries the migrated workers.
	raw, err := os.ReadFile(filepath.Join(dir, "project.json"))
	if err != nil {
		t.Fatalf("read project.json: %v", err)
	}
	var p Project
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("parse project.json: %v", err)
	}
	if p.ID != "default" || len(p.Workers) != 2 {
		t.Fatalf("project = %+v, want id=default with 2 workers", p)
	}
	var niq WorkerConfig
	for _, w := range p.Workers {
		if w.ID == "niq" {
			niq = w
		}
	}
	if niq.Type != "reason" || niq.Model != "deepseek-v4-flash" || niq.Provider != "deepseek" {
		t.Fatalf("niq worker = %+v, want reason/deepseek-v4-flash", niq)
	}

	// Idempotent: second migrate errors because the project exists.
	if _, err := MigrateLegacyProject("default", base); err == nil {
		t.Fatal("expected error on duplicate migrate")
	}
}