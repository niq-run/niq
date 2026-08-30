package swarm

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/worker"
	"github.com/niq-run/niq/pkg/service/eventbus"
	"github.com/niq-run/niq/pkg/service/eventbus/transport/inprocess"
	"github.com/niq-run/niq/pkg/service/workerhost"
)

// newTestEngine wires up a registry, engine and in-process listener exactly as
// RunSwarm does, returning the build context, worker service and the store dir.
func newTestEngine(t *testing.T) (BuildContext, *workerhost.WorkerService, string) {
	t.Helper()
	dir := t.TempDir()

	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "id", "identities.json"))
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

	storeDir := filepath.Join(dir, "state", "workers")
	svc := newServiceWithStore(t, storeDir)
	bc := BuildContext{
		Registry:  registry,
		Listener:  listener,
		Engine:    engine,
		WorkerSvc: svc,
	}
	RegisterBuilders(bc, svc)
	return bc, svc, storeDir
}

func newServiceWithStore(t *testing.T, storeDir string) *workerhost.WorkerService {
	t.Helper()
	svc := workerhost.New()
	store, err := NewFileWorkerStore(storeDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	svc.SetStore(store)
	return svc
}

func TestWorkspaceSuspendResume(t *testing.T) {
	_, svc, _ := newTestEngine(t)
	ctx := context.Background()

	if err := svc.CreateWorker(ctx, worker.WorkerConfig{
		Type: "workspace", Params: map[string]any{"root_dir": t.TempDir()},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	wsID := firstWorkerOfType(svc, "workspace")
	if wsID == "" {
		t.Fatal("no workspace worker created")
	}

	if info := workerStatus(svc, wsID); info.State != worker.StateRunning {
		t.Fatalf("expected running, got %s", info.State)
	}

	if err := svc.SuspendWorker(wsID); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	if info := workerStatus(svc, wsID); info.State != worker.StateSuspended {
		t.Fatalf("expected suspended, got %s", info.State)
	}

	if err := svc.ResumeWorker(ctx, wsID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if info := workerStatus(svc, wsID); info.State != worker.StateRunning {
		t.Fatalf("expected running after resume, got %s", info.State)
	}
}

// TestSpawnedWorkerSurvivesRestart verifies that a spawned (non-config) worker,
// once persisted, is re-materialized as suspended after a process restart.
func TestSpawnedWorkerSurvivesRestart(t *testing.T) {
	bc, svc, storeDir := newTestEngine(t)
	ctx := context.Background()

	if err := svc.CreateWorker(ctx, worker.WorkerConfig{
		Type: "workspace", Params: map[string]any{"root_dir": t.TempDir()},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	wsID := firstWorkerOfType(svc, "workspace")
	if wsID == "" {
		t.Fatal("no workspace worker created")
	}
	if err := svc.SuspendWorker(wsID); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// Simulate a fresh process: new WorkerService + builders over the same store.
	svc2 := newServiceWithStore(t, storeDir)
	RegisterBuilders(bc, svc2)

	recs, err := svc2.LoadAllWorkers()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var found bool
	for _, rec := range recs {
		if rec.ID == wsID {
			found = true
			if rec.State != worker.StateSuspended {
				t.Fatalf("expected persisted suspended, got %s", rec.State)
			}
		}
	}
	if !found {
		t.Fatalf("worker %s not persisted", wsID)
	}

	// RestoreSuspended re-materializes it as suspended (not running).
	if err := svc2.RestoreSuspended(worker.WorkerConfig{ID: wsID, Type: "workspace", Params: recParams(svc2, wsID)}, nil); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if info := workerStatus(svc2, wsID); info.State != worker.StateSuspended {
		t.Fatalf("expected suspended after restore, got %s", info.State)
	}

	// It can be resumed on the fresh service.
	if err := svc2.ResumeWorker(ctx, wsID); err != nil {
		t.Fatalf("resume after restart: %v", err)
	}
	if info := workerStatus(svc2, wsID); info.State != worker.StateRunning {
		t.Fatalf("expected running after resume, got %s", info.State)
	}
}

func recParams(svc *workerhost.WorkerService, id string) map[string]any {
	recs, _ := svc.LoadAllWorkers()
	for _, r := range recs {
		if r.ID == id {
			return r.Params
		}
	}
	return nil
}

func firstWorkerOfType(svc *workerhost.WorkerService, typ string) string {
	for _, wi := range svc.ListWorkers("") {
		if wi.Type == typ {
			return wi.ID
		}
	}
	return ""
}

func workerStatus(svc *workerhost.WorkerService, id string) workerhost.WorkerInfo {
	for _, wi := range svc.ListWorkers("") {
		if wi.ID == id {
			return wi
		}
	}
	return workerhost.WorkerInfo{}
}
