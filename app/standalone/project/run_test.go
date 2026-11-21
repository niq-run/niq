package project

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/app/standalone/store"
	"github.com/niq-run/niq/app/standalone/store/identity"
	projworker "github.com/niq-run/niq/app/standalone/worker"
	projprocess "github.com/niq-run/niq/app/standalone/worker/process"
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/worker"
)

// idWorker is a ManagedWorker with no state of its own.
type idWorker struct {
	baseworker.BaseWorker
}

func (w *idWorker) Stop() error               { return nil }
func (w *idWorker) Snapshot() ([]byte, error) { return nil, nil }
func (w *idWorker) Restore([]byte) error      { return nil }

// fakeType builds a trivial in-process worker: enough to exercise the state
// store through the real control plane.
type fakeType struct {
	registry corebus.IdentityRegistry
	listener *inprocess.InProcListener
}

func (f fakeType) build(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	id := cfg.ID
	connect := func() (corebus.WorkerSideChannel, error) {
		if err := projprocess.RegisterIdentity(f.registry, corebus.Identity{WorkerID: id, Type: cfg.Type}); err != nil {
			return nil, err
		}
		ch := inprocess.NewWorkerSide(id, f.listener)
		if err := ch.Connect(context.Background(), "inproc://test"); err != nil {
			return nil, err
		}
		return ch, nil
	}
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		return &idWorker{BaseWorker: baseworker.NewBaseWorker(id, ch)}
	}
	return worker.SpawnSpec{Config: cfg, Connect: connect, Build: build}, nil
}

// TestStateStorePersistsAndRecovers exercises the concrete project StateStore
// through the control plane: suspend persists state.json, and a fresh control
// plane over the same store recovers the manifest as suspended.
func TestStateStorePersistsAndRecovers(t *testing.T) {
	// The manifest store writes through the project layout, so isolate HOME.
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("p", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	dir := t.TempDir()
	registry := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
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

	stateStore, err := store.NewState(filepath.Join(dir, "workers"))
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	identityPath := filepath.Join(dir, "identities.json")
	newCP := func() *projworker.ControlPlane {
		ids, err := identity.New(identityPath)
		if err != nil {
			t.Fatalf("identity store: %v", err)
		}
		cp := projworker.New(projworker.Config{
			ProjectID:  "p",
			Manifests:  store.NewManifest(store.ProjectDir("p")),
			StateStore: stateStore,
			Registry:   registry,
			Bus:        busctl.New(ids, registry),
		})
		ft := fakeType{registry: registry, listener: listener}
		cp.RegisterBuilder("probe", ft.build, nil)
		return cp
	}

	cp := newCP()
	if _, err := cp.Manager().Create(ctx, worker.WorkerConfig{ID: "w1", Type: "probe"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := cp.Manager().Suspend("w1"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	recs, err := stateStore.LoadAll()
	if err != nil || len(recs) != 1 || recs[0].State != worker.StateSuspended {
		t.Fatalf("store records = %+v (%v)", recs, err)
	}

	// A fresh control plane over the same store recovers the declared worker.
	cp2 := newCP()
	if err := cp2.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	var state string
	for _, wi := range cp2.Manager().List("") {
		if wi.ID == "w1" {
			state = wi.State
		}
	}
	if state != string(worker.StateSuspended) {
		t.Fatalf("recovered state = %q, want suspended", state)
	}
	if err := cp2.Manager().Resume(ctx, "w1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !cp2.Manager().Has("w1") {
		t.Fatal("resumed worker is not running")
	}

	// Destroying through the control plane removes the state directory.
	if err := cp2.HostBackend().Destroy(ctx, "w1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if recs, _ := stateStore.LoadAll(); len(recs) != 0 {
		t.Fatalf("destroy left state records: %+v", recs)
	}
}

// TestProjectManifestStore exercises the concrete workers.json ManifestStore.
func TestProjectManifestStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := CreateProject("p", "", nil); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	manifests := store.NewManifest(store.ProjectDir("p"))
	m := projworker.Manifest{Definition: projworker.Definition{ID: "w", Type: "timer"}}
	if err := manifests.Add(&m); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok, err := manifests.Get("w"); err != nil || !ok {
		t.Fatalf("Get = %v %v", ok, err)
	}
	if active, err := manifests.List(false); err != nil || len(active) != 1 {
		t.Fatalf("active list = %v %v", active, err)
	}
	if err := manifests.Archive("w", true); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if archived, err := manifests.List(true); err != nil || len(archived) != 1 {
		t.Fatalf("archived list = %v %v", archived, err)
	}
	if err := manifests.Delete("w"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, _ := manifests.Get("w"); ok {
		t.Fatal("delete left manifest")
	}
}
