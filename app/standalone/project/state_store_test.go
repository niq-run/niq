package project

import (
	"context"
	"path/filepath"
	"testing"

	projworker "github.com/niq-run/niq/app/standalone/worker"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workersvc"
	"github.com/niq-run/niq/core/itfs/worker"
)

// TestStateStorePersistsAndRecovers exercises the concrete project StateStore
// through the in-process lifecycle: suspend persists state.json, a fresh
// WorkerService over the same store recovers the declaration.
func TestStateStorePersistsAndRecovers(t *testing.T) {
	dir := t.TempDir()
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	engine := eventbus.NewEngine(registry, eventbus.NewMemoryEventStore())
	listener := inprocess.NewInProcListener()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			ch, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
		}
	}()

	store, err := NewStateStore(filepath.Join(dir, "workers"))
	if err != nil {
		t.Fatalf("NewStateStore: %v", err)
	}
	svc := workersvc.New()
	svc.SetStore(store)
	bc := projworker.BuildContext{Registry: registry, Listener: listener, Engine: engine, WorkerSvc: svc}
	projworker.RegisterBuilders(bc, svc)

	mount := t.TempDir()
	resolved, err := svc.CreateWorker(ctx, worker.WorkerConfig{
		Type: "workspace", Params: map[string]any{"mounts": []string{mount}},
	})
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	id := resolved.ID
	if id == "" {
		t.Fatal("workspace builder did not resolve an id")
	}
	if err := svc.SuspendWorker(id); err != nil {
		t.Fatalf("SuspendWorker: %v", err)
	}
	recs, err := store.LoadAll()
	if err != nil || len(recs) != 1 || recs[0].State != worker.StateSuspended {
		t.Fatalf("store records = %+v (%v)", recs, err)
	}

	// A fresh service over the same store recovers the declared worker.
	svc2 := workersvc.New()
	svc2.SetStore(store)
	bc2 := projworker.BuildContext{Registry: registry, Listener: listener, Engine: engine, WorkerSvc: svc2}
	projworker.RegisterBuilders(bc2, svc2)
	if err := svc2.RecoverAll(ctx, workersvc.RecoverOptions{
		Workers: []worker.WorkerConfig{{ID: id, Type: "workspace", Params: map[string]any{"mounts": []string{mount}}}},
	}); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}
	var state string
	for _, wi := range svc2.ListWorkers("") {
		if wi.ID == id {
			state = string(wi.State)
		}
	}
	if state != string(worker.StateSuspended) {
		t.Fatalf("recovered state = %q, want suspended", state)
	}
	if err := svc2.ResumeWorker(ctx, id); err != nil {
		t.Fatalf("ResumeWorker: %v", err)
	}
	if _, ok := svc2.Worker(id); !ok {
		t.Fatal("resumed worker is not running")
	}

	// Destroying through the service removes the state directory.
	if err := svc2.DestroyWorker(id); err != nil {
		t.Fatalf("DestroyWorker: %v", err)
	}
	if recs, _ := store.LoadAll(); len(recs) != 0 {
		t.Fatalf("destroy left state records: %+v", recs)
	}
}

// TestProjectDeclStore exercises the concrete workers.json DeclStore.
func TestProjectDeclStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("p", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	store := projectDeclStore{projectID: "p"}
	d := projworker.Declaration{Config: projworker.Config{ID: "w", Type: "timer"}}
	if err := store.Add(&d); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok, err := store.Get("w"); err != nil || !ok {
		t.Fatalf("Get = %v %v", ok, err)
	}
	if err := store.SetCredential("w", "tok"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	if got, _, _ := store.Get("w"); got.Credential != "tok" {
		t.Fatalf("credential not persisted: %+v", got)
	}
	if err := store.Delete("w"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := store.Get("w"); ok {
		t.Fatal("delete left declaration")
	}
}
