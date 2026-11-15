package project

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	projworker "github.com/niq-run/niq/app/project/worker"
	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

func newSpawnArgs(t *testing.T) webuiSpawnArgs {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("proj", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return webuiSpawnArgs{projectID: "proj"}
}

// TestSpawnArgsFlattenParams verifies the create form's nested params become the
// host's top-level spawn arguments, and nothing is persisted by the translation.
func TestSpawnArgsFlattenParams(t *testing.T) {
	a := newSpawnArgs(t)
	args, err := a.SpawnArgs(json.RawMessage(
		`{"type":"reason","id":"r1","placement":"in-process","label":"值班","tags":["ops"],` +
			`"publish":[{"type":"worker.ready"}],"params":{"instruction":"do x","model":"m"}}`))
	if err != nil {
		t.Fatalf("SpawnArgs: %v", err)
	}
	if _, nested := args["params"]; nested {
		t.Fatalf("params stayed nested: %v", args)
	}
	if args["type"] != "reason" || args["id"] != "r1" || args["instruction"] != "do x" || args["model"] != "m" {
		t.Fatalf("args = %v", args)
	}
	if args["label"] != "值班" {
		t.Fatalf("display metadata lost: %v", args)
	}
	// Translation is pure: no declaration was written.
	set, _ := LoadWorkers("proj")
	if _, ok := FindWorker(set, "r1"); ok {
		t.Fatal("SpawnArgs persisted a declaration; the host owns that")
	}
}

// TestSpawnArgsProcessWorker verifies launch fields pass through untouched and
// an env-shaped params object still flattens (a managed-process child's params
// ARE its environment).
func TestSpawnArgsProcessWorker(t *testing.T) {
	a := newSpawnArgs(t)
	args, err := a.SpawnArgs(json.RawMessage(
		`{"type":"mcp","id":"e1","placement":"managed-process","command":["/bin/sh","-c","sleep 60"],"cwd":"/tmp","params":{"K":"V"}}`))
	if err != nil {
		t.Fatalf("SpawnArgs: %v", err)
	}
	cmd, _ := args["command"].([]any)
	if len(cmd) != 3 || args["cwd"] != "/tmp" || args["K"] != "V" {
		t.Fatalf("args = %v", args)
	}
}

// TestSpawnArgsRejectsCollision verifies a param that collides with a launch
// field is refused instead of silently overriding it.
func TestSpawnArgsRejectsCollision(t *testing.T) {
	a := newSpawnArgs(t)
	_, err := a.SpawnArgs(json.RawMessage(`{"type":"reason","id":"r1","params":{"id":"other"}}`))
	if err == nil || !strings.Contains(err.Error(), "collides") {
		t.Fatalf("err = %v, want a collision error", err)
	}
}

// TestSpawnArgsInstantiatesProject verifies the {project} placeholder is resolved
// for the form, exactly as it is for a template-declared worker.
func TestSpawnArgsInstantiatesProject(t *testing.T) {
	a := newSpawnArgs(t)
	args, err := a.SpawnArgs(json.RawMessage(`{"type":"workspace","id":"ws","params":{"path":"/tmp/{project}/ws"}}`))
	if err != nil {
		t.Fatalf("SpawnArgs: %v", err)
	}
	if args["path"] != "/tmp/proj/ws" {
		t.Fatalf("path = %v, want the project id substituted", args["path"])
	}
}

// TestIdentityAdminPersistsThenApplies verifies an ACL edit writes the
// declaration first and then the runtime registry — one owner for the span.
func TestIdentityAdminPersistsThenApplies(t *testing.T) {
	proj := newSpawnArgs(t) // creates project "proj"
	_ = proj
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(corebus.Identity{WorkerID: "r1", Type: "reason"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	admin := identityAdmin{projectID: "proj", registry: registry}

	pub := []event.PublishPattern{event.NewPublishPattern("worker.ready")}
	sub := []event.EventPattern{event.NewPattern("worker.discover")}
	if err := admin.UpdateACL("r1", pub, sub); err == nil {
		t.Fatal("UpdateACL succeeded for an undeclared worker")
	}

	d := projworker.Declaration{Config: projworker.Config{Type: "reason", ID: "r1"}}
	if err := (projectDeclStore{projectID: "proj"}).Add(&d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := admin.UpdateACL("r1", pub, sub); err != nil {
		t.Fatalf("UpdateACL: %v", err)
	}
	got, _, _ := (projectDeclStore{projectID: "proj"}).Get("r1")
	if len(got.Publish) != 1 || got.Publish[0].Type != "worker.ready" {
		t.Fatalf("declaration publish = %+v", got.Publish)
	}
	idt, ok := registry.Lookup("r1")
	if !ok || len(idt.PublishAllow) != 1 || len(idt.SubscribeAllow) != 1 {
		t.Fatalf("registry identity = %+v, ok=%v", idt, ok)
	}
}

// TestWorkerDeclsIsLive verifies the read projection reflects the files without
// a restart.
func TestWorkerDeclsIsLive(t *testing.T) {
	a := newSpawnArgs(t)
	_ = a
	decls := webuiDecls{projectID: "proj"}
	if got := decls.WorkerDecls(); len(got) != 0 {
		t.Fatalf("initial decls = %+v, want empty", got)
	}
	d := projworker.Declaration{
		Config: projworker.Config{Type: "reason", ID: "r1", Params: map[string]any{"model": "m"}},
		Label:  "值班",
	}
	if err := (projectDeclStore{projectID: "proj"}).Add(&d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := decls.WorkerDecls()
	if got["r1"].Label != "值班" || got["r1"].Params["model"] != "m" {
		t.Fatalf("decls = %+v", got["r1"])
	}

	// An archived worker leaves the active projection: showing its label and
	// params next to live workers would be misleading.
	if err := SetWorkerArchived("proj", "r1", true); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if got := decls.WorkerDecls(); len(got) != 0 {
		t.Fatalf("decls after archive = %+v, want empty", got)
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
