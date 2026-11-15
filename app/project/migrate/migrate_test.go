package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
}

func readMap(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnsureProjectLegacy(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "project.json"), map[string]any{
		"id": "sep5", "created_at": "2026-01-01T00:00:00Z", "ports": map[string]any{"bus": 123, "webui": 456},
		"archived": map[string]any{"retired": true},
		"workers": []any{
			map[string]any{"type": "reason", "id": "niq", "managed": true, "instruction": "do x", "provider": "volc",
				"params": map[string]any{"goal": "g"}, "subscriptions": []any{"worker.ready"}},
			map[string]any{"type": "mcp", "id": "proc", "managed": false, "command": []any{"x"}, "env": map[string]any{"K": "V"},
				"params": map[string]any{"TOKEN": "t"}, "token_ttl_seconds": 10},
			map[string]any{"type": "mcp", "id": "remote", "managed": false, "remote": true, "publish": []any{"*"}},
			map[string]any{"type": "timer", "id": "old", "archived": true},
			map[string]any{"type": "timer", "id": "retired"},
		},
	})
	write(t, filepath.Join(dir, "id", "identities.json"), map[string]any{
		"niq": map[string]any{
			"WorkerID": "niq", "Type": "reason", "Credential": "",
			"SubscribeAllow": []any{map[string]any{"type": "worker.input", "source": "timer"}},
		},
		"remote": map[string]any{
			"WorkerID": "remote", "Type": "mcp", "Credential": "tok-secret", "Remote": true,
			"PublishAllow": []any{map[string]any{"type": "*", "target": "*"}},
		},
		"ghost": map[string]any{"WorkerID": "ghost", "Type": "timer", "Credential": "x"},
	})
	if err := os.WriteFile(filepath.Join(dir, "id", "root_secret"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := EnsureProject(dir); err != nil {
		t.Fatalf("EnsureProject: %v", err)
	}
	meta := readMap(t, filepath.Join(dir, "project.json"))
	if meta["schema_version"] != float64(2) || meta["id"] != "sep5" {
		t.Fatalf("project.json = %+v", meta)
	}
	if _, ok := meta["workers"]; ok {
		t.Fatal("workers leaked into project.json")
	}

	active := readMap(t, filepath.Join(dir, "workers.json"))
	activeWorkers := active["workers"].([]any)
	if len(activeWorkers) != 3 { // niq, proc, remote
		t.Fatalf("active workers = %d", len(activeWorkers))
	}
	byID := map[string]map[string]any{}
	for _, w := range activeWorkers {
		m := w.(map[string]any)
		byID[m["id"].(string)] = m
	}
	niq := byID["niq"]
	if niq["placement"] != "in-process" {
		t.Fatalf("niq placement = %v", niq["placement"])
	}
	if len(niq["params"].(map[string]any)) != 3 { // instruction/provider/goal
		t.Fatalf("niq params = %+v", niq["params"])
	}
	if len(niq["subscriptions"].([]any)) != 1 {
		t.Fatalf("niq subscriptions = %+v", niq["subscriptions"])
	}

	proc := byID["proc"]
	if proc["placement"] != "managed-process" || proc["params"].(map[string]any)["K"] != "V" {
		t.Fatalf("proc = %+v", proc)
	}
	if _, ok := proc["credential"]; ok {
		t.Fatalf("managed-process credential persisted: %+v", proc)
	}
	if _, ok := proc["token_ttl_seconds"]; ok {
		t.Fatalf("managed-process ttl persisted: %+v", proc)
	}

	remote := byID["remote"]
	if remote["placement"] != "unmanaged-process" || remote["credential"] != "tok-secret" {
		t.Fatalf("remote = %+v", remote)
	}

	arch := readMap(t, filepath.Join(dir, "workers.archived.json"))
	archWorkers := arch["workers"].([]any)
	if len(archWorkers) != 2 { // old, retired
		t.Fatalf("archived = %d", len(archWorkers))
	}

	if _, err := os.Stat(filepath.Join(dir, "root_secret")); err != nil {
		t.Fatalf("root_secret not at project root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "id", "identities.json")); err == nil {
		t.Fatal("identities.json still present")
	}
	if _, err := os.Stat(filepath.Join(dir, "id")); err == nil {
		t.Fatal("empty id dir not removed")
	}
	report := readMap(t, filepath.Join(dir, "migration-report.json"))
	orphans := report["orphan_identities"].([]any)
	if len(orphans) != 1 || orphans[0] != "ghost" {
		t.Fatalf("orphans = %+v", orphans)
	}

	// Idempotent.
	if err := EnsureProject(dir); err != nil {
		t.Fatalf("second EnsureProject: %v", err)
	}
}

func TestEnsureTemplates(t *testing.T) {
	dir := t.TempDir()
	tmplDir := filepath.Join(dir, "legacy")
	write(t, filepath.Join(tmplDir, "template.json"), map[string]any{
		"workers": []any{
			map[string]any{"type": "reason", "id": "r", "instruction": "do x", "mounts": []any{"/tmp/x"}},
			map[string]any{"type": "mcp", "id": "p", "managed": false, "command": []any{"x"}, "env": map[string]any{"K": "V"}},
		},
	})
	if err := EnsureTemplates(dir); err != nil {
		t.Fatalf("EnsureTemplates: %v", err)
	}
	m := readMap(t, filepath.Join(tmplDir, "template.json"))
	if m["schema_version"] != float64(2) {
		t.Fatalf("template = %+v", m)
	}
	workers := m["workers"].([]any)
	r := workers[0].(map[string]any)
	if r["params"].(map[string]any)["instruction"] != "do x" {
		t.Fatalf("reason params = %+v", r["params"])
	}
	p := workers[1].(map[string]any)
	if p["placement"] != "managed-process" || p["params"].(map[string]any)["K"] != "V" {
		t.Fatalf("process worker = %+v", p)
	}
}
