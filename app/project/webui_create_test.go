package project

import (
	"encoding/json"
	"strings"
	"testing"

	projworker "github.com/niq-run/niq/app/project/worker"
)

func newDeclCreator(t *testing.T) *webuiDeclCreator {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("proj", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &webuiDeclCreator{store: projectDeclStore{projectID: "proj"}, projectID: "proj"}
}

func TestDeclCreatorInProcessPersistsOnly(t *testing.T) {
	c := newDeclCreator(t)
	created, err := c.Create(json.RawMessage(
		`{"type":"reason","id":"r1","placement":"in-process","params":{"instruction":"do x","model":"m"}}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.Managed || created.Placement != string(projworker.PlacementInProcess) {
		t.Fatalf("created = %+v, want in-process", created)
	}
	set, _ := LoadWorkers("proj")
	spec, ok := FindWorker(set, "r1")
	if !ok {
		t.Fatal("declaration not persisted")
	}
	if !projworker.IsInProcess(spec) || len(spec.Command) != 0 {
		t.Fatalf("declaration = %+v, want an in-process entry", spec)
	}
	if spec.Params["instruction"] != "do x" || spec.Params["model"] != "m" {
		t.Fatalf("params = %+v", spec.Params)
	}
}

func TestDeclCreatorManagedProcess(t *testing.T) {
	c := newDeclCreator(t)
	created, err := c.Create(json.RawMessage(
		`{"type":"mcp","id":"e1","placement":"managed-process","command":["/bin/sh","-c","sleep 60"],"params":{"K":"V"}}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Managed || created.Remote || created.Placement != string(projworker.PlacementManagedProcess) {
		t.Fatalf("created = %+v, want managed-process", created)
	}
	set, _ := LoadWorkers("proj")
	spec, ok := FindWorker(set, "e1")
	if !ok || projworker.IsInProcess(spec) || len(spec.Command) == 0 || spec.Params["K"] != "V" {
		t.Fatalf("declaration = %+v", spec)
	}
	if spec.Credential != "" {
		t.Fatalf("creator minted a credential: %q", spec.Credential)
	}
}

func TestDeclCreatorUnmanagedProcess(t *testing.T) {
	c := newDeclCreator(t)
	created, err := c.Create(json.RawMessage(
		`{"type":"mcp","id":"r3","placement":"unmanaged-process"}`))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Managed || !created.Remote || created.Placement != string(projworker.PlacementUnmanagedProcess) {
		t.Fatalf("created = %+v, want unmanaged-process", created)
	}
	set, _ := LoadWorkers("proj")
	spec, ok := FindWorker(set, "r3")
	if !ok || projworker.IsInProcess(spec) || len(spec.Command) != 0 {
		t.Fatalf("declaration = %+v", spec)
	}
}

func TestDeclCreatorValidation(t *testing.T) {
	c := newDeclCreator(t)
	if _, err := c.Create(json.RawMessage(`{"type":"reason","id":"r1","placement":"in-process"}`)); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	for _, tt := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"duplicate", map[string]any{"type": "reason", "id": "r1"}, "already exists"},
		{"missing type", map[string]any{"id": "r2"}, "type is required"},
		{"missing id", map[string]any{"type": "reason"}, "id is required"},
		{"invalid id", map[string]any{"type": "reason", "id": "a b"}, "id may only contain"},
		{"managed-process without command", map[string]any{"type": "mcp", "id": "e1", "placement": "managed-process"}, "command is required"},
		{"managed-process non-string param", map[string]any{"type": "mcp", "id": "e2", "placement": "managed-process", "command": []string{"x"}, "params": map[string]any{"K": 1}}, "must be a string"},
		{"unmanaged-process with command", map[string]any{"type": "mcp", "id": "e3", "placement": "unmanaged-process", "command": []string{"x"}}, "must not carry a command"},
		{"unmanaged-process with params", map[string]any{"type": "mcp", "id": "e4", "placement": "unmanaged-process", "params": map[string]any{"K": "V"}}, "cannot carry params"},
		{"bad tag", map[string]any{"type": "reason", "id": "r3", "tags": []string{"a//b"}}, "empty segment"},
	} {
		raw, _ := json.Marshal(tt.body)
		_, err := c.Create(raw)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

func TestDeclStoreRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("proj", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	store := projectDeclStore{projectID: "proj"}
	d := projworker.Declaration{Config: projworker.Config{Type: "reason", ID: "r1"}}
	if err := store.Add(&d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := store.Add(&d); err == nil {
		t.Fatal("Add accepted a duplicate id")
	}
	got, ok, err := store.Get("r1")
	if err != nil || !ok || got.Type != "reason" {
		t.Fatalf("Get = (%+v, %v, %v)", got, ok, err)
	}
	if err := store.SetCredential("r1", "tok"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	got, _, _ = store.Get("r1")
	if got.Credential != "tok" {
		t.Fatalf("credential not persisted: %q", got.Credential)
	}
	got.Label = "值班"
	if err := store.Update(got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if again, _, _ := store.Get("r1"); again.Label != "值班" {
		t.Fatalf("update not persisted: %+v", again)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = (%v, %v)", list, err)
	}
	if err := store.Delete("r1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := store.Get("r1"); ok {
		t.Fatal("declaration survived delete")
	}
}
