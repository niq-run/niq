package project

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/internal/webui"
	"github.com/niq-run/niq/pkg/eventbus"
	"github.com/niq-run/niq/pkg/services/workerhost"
)

// newDeclCreator builds a webuiDeclCreator over a fresh temp project with no
// initial workers, plus its registry / supervisor / workerSvc.
func newDeclCreator(t *testing.T) (*webuiDeclCreator, *UnmanagedSupervisor) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("proj", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "id", "identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	sv := testSupervisor()
	return &webuiDeclCreator{
		supervisor: sv,
		registry:   registry,
		workerSvc:  workerhost.New(),
		projectID:  "proj",
	}, sv
}

// TestDeclCreatorManaged verifies a managed declaration is persisted as a
// light project.json entry, its authoritative config.json is seeded from the
// form body, and the spawn payload carries the flattened builder params.
func TestDeclCreatorManaged(t *testing.T) {
	c, _ := newDeclCreator(t)
	created, err := c.Create(json.RawMessage(`{"type":"reason","id":"r1","instruction":"do x","model":"m"}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.Managed {
		t.Fatalf("created = %+v, want managed", created)
	}

	p, err := LoadProject("proj")
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := FindWorker(p, "r1")
	if !ok {
		t.Fatal("declaration not persisted")
	}
	if !spec.Managed || len(spec.Command) != 0 {
		t.Fatalf("declaration = %+v, want a light managed entry", spec)
	}

	cfg, ok := readWorkerConfig(ProjectDir("proj"), "r1")
	if !ok {
		t.Fatal("config.json not seeded")
	}
	if cfg.Type != "reason" {
		t.Fatalf("config type = %q", cfg.Type)
	}
	if cfg.Params["instruction"] != "do x" || cfg.Params["model"] != "m" {
		t.Fatalf("config params = %v", cfg.Params)
	}

	// Spawn payload: {type,id} plus the flattened params.
	if created.Spawn["type"] != "reason" || created.Spawn["id"] != "r1" ||
		created.Spawn["instruction"] != "do x" || created.Spawn["model"] != "m" {
		t.Fatalf("spawn payload = %v", created.Spawn)
	}

	// ManagedSpawn rebuilds the same payload from config.json.
	payload, managed, err := c.ManagedSpawn("r1")
	if err != nil || !managed {
		t.Fatalf("ManagedSpawn = (%v, %v, %v)", payload, managed, err)
	}
	if payload["instruction"] != "do x" {
		t.Fatalf("ManagedSpawn payload = %v", payload)
	}
}

// TestDeclCreatorExternal verifies an external declaration is persisted with
// its command, provisioned (credential generated and persisted back), and the
// supervisor reports it running.
func TestDeclCreatorExternal(t *testing.T) {
	c, sv := newDeclCreator(t)
	created, err := c.Create(json.RawMessage(
		`{"type":"mcp","id":"e1","managed":false,"command":["/bin/sh","-c","sleep 60"]}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Managed || created.Spawn != nil {
		t.Fatalf("created = %+v, want a plain external result", created)
	}

	p, err := LoadProject("proj")
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := FindWorker(p, "e1")
	if !ok || spec.Managed || len(spec.Command) == 0 {
		t.Fatalf("declaration = %+v, want an external entry with command", spec)
	}
	if spec.Credential == "" {
		t.Fatal("credential not persisted")
	}
	if _, ok := c.registry.Lookup("e1"); !ok {
		t.Fatal("identity not registered")
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if st := findStatus(sv.List(), "e1"); st != nil && st.State == "running" {
			sv.Shutdown()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	sv.Shutdown()
	t.Fatalf("supervisor never reported e1 running: %v", sv.List())
}

// TestDeclCreatorValidation covers the create-time rejections: duplicate ids
// across declarations / worker service / registry, missing fields, invalid id
// characters, and a commandless external worker.
func TestDeclCreatorValidation(t *testing.T) {
	c, _ := newDeclCreator(t)
	body := map[string]any{"type": "reason", "id": "r1"}
	raw, _ := json.Marshal(body)
	if _, err := c.Create(raw); err != nil {
		t.Fatalf("seed create: %v", err)
	}

	for _, tt := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"duplicate declaration", map[string]any{"type": "reason", "id": "r1"}, "already exists"},
		{"duplicate worker service", map[string]any{"type": "reason", "id": "r1", "managed": true}, "already exists"},
		{"missing type", map[string]any{"id": "r2"}, "type is required"},
		{"missing id", map[string]any{"type": "reason"}, "id is required"},
		{"invalid id", map[string]any{"type": "reason", "id": "a b"}, "id may only contain"},
		{"external without command", map[string]any{"type": "mcp", "id": "e1", "managed": false}, "command is required"},
	} {
		raw, _ := json.Marshal(tt.body)
		_, err := c.Create(raw)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to contain %q", tt.name, err, tt.want)
		}
	}
}

// TestDeclaredIncludesManaged verifies Declared() reports both declaration
// kinds, with managed entries always in the stopped state.
func TestDeclaredIncludesManaged(t *testing.T) {
	c, _ := newDeclCreator(t)
	if _, err := c.Create(json.RawMessage(`{"type":"reason","id":"r1"}`)); err != nil {
		t.Fatalf("create managed: %v", err)
	}
	if _, err := c.Create(json.RawMessage(
		`{"type":"mcp","id":"e1","managed":false,"command":["/bin/sh","-c","sleep 60"]}`)); err != nil {
		t.Fatalf("create external: %v", err)
	}

	a := &webuiUnmanagedAdapter{supervisor: c.supervisor, registry: c.registry, projectID: "proj"}
	declared := a.Declared()
	c.supervisor.Shutdown()
	m := findStatus2(declared, "r1")
	if m == nil || !m.Managed || m.State != "stopped" {
		t.Fatalf("managed entry = %+v, want managed/stopped", m)
	}
	// The creator started the external worker, so its live state is running.
	e := findStatus2(declared, "e1")
	if e == nil || e.Managed || e.State != "running" {
		t.Fatalf("external entry = %+v, want unmanaged/running", e)
	}
}

// TestManagedSpawnUnknownID verifies the lookup is silent for ids that are not
// declared managed workers, so the start endpoint falls through to the
// external path.
func TestManagedSpawnUnknownID(t *testing.T) {
	c, _ := newDeclCreator(t)
	if payload, managed, err := c.ManagedSpawn("nobody"); managed || payload != nil || err != nil {
		t.Fatalf("ManagedSpawn(nobody) = (%v, %v, %v)", payload, managed, err)
	}
}

func findStatus2(sts []webui.UnmanagedStatus, id string) *webui.UnmanagedStatus {
	for i := range sts {
		if sts[i].ID == id {
			return &sts[i]
		}
	}
	return nil
}
