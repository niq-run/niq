package workerhost

import (
	"context"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/worker"
)

// TestRestartWorkerKeepsState verifies a restart replays the persisted snapshot
// onto the freshly built instance: construction inputs come from the new config,
// state the worker already owns survives. That is the same sequence recovery and
// resume use, so every way of bringing a worker up means one thing.
func TestRestartWorkerKeepsState(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)
	ctx := context.Background()

	if _, err := svc.CreateWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "old"}}); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	// Suspend so the store holds a snapshot and the worker is not running: a
	// restart must still replace it (suspended entries are restartable).
	if err := svc.SuspendWorker("w"); err != nil {
		t.Fatalf("SuspendWorker: %v", err)
	}
	store := svc.store.(*memoryStore)
	if rec := store.recs["w"]; len(rec.Snapshot) == 0 || rec.State != worker.StateSuspended {
		t.Fatalf("store after suspend = %+v, want a suspended snapshot", rec)
	}

	if err := svc.RestartWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "new"}}); err != nil {
		t.Fatalf("RestartWorker: %v", err)
	}

	if len(*built) != 2 {
		t.Fatalf("built %d workers, want a fresh instance (2)", len(*built))
	}
	fresh := (*built)[1]
	if string(fresh.restored) != "snap" {
		t.Fatalf("fresh worker restored %q, want the persisted snapshot \"snap\"", fresh.restored)
	}
	if !fresh.started {
		t.Fatal("fresh worker was not started")
	}
	if got := workerStatus(svc, "w"); got.State != worker.StateRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
	// The snapshot must survive: state is what the worker owns, not something a
	// restart is allowed to throw away.
	if rec := store.recs["w"]; len(rec.Snapshot) == 0 || rec.State != worker.StateRunning {
		t.Fatalf("store after restart = %+v, want a running record carrying the snapshot", rec)
	}
}

// TestRestartWorkerSnapshotsRunning verifies a restart of a live instance
// snapshots it first, so nothing the worker learned between starts is lost.
func TestRestartWorkerSnapshotsRunning(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)
	ctx := context.Background()

	if _, err := svc.CreateWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake"}); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	// Newer state than the store holds (spawn persisted no snapshot).
	(*built)[0].snap = []byte("learned-since-start")

	if err := svc.RestartWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "new"}}); err != nil {
		t.Fatalf("RestartWorker: %v", err)
	}
	if len(*built) != 2 {
		t.Fatalf("built %d workers, want 2", len(*built))
	}
	if got := string((*built)[1].restored); got != "learned-since-start" {
		t.Fatalf("restored = %q, want the live instance's snapshot", got)
	}
	if rec := svc.store.(*memoryStore).recs["w"]; string(rec.Snapshot) != "learned-since-start" {
		t.Fatalf("store snapshot = %q, want the pre-restart snapshot", rec.Snapshot)
	}
}

// TestRestartWorkerTypeChangeDropsSnapshot verifies a type change does not replay
// a blob that belongs to a different implementation.
func TestRestartWorkerTypeChangeDropsSnapshot(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	_, other := registerTwoFakeBuilders(svc)
	ctx := context.Background()

	if _, err := svc.CreateWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake"}); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	if err := svc.SuspendWorker("w"); err != nil {
		t.Fatalf("SuspendWorker: %v", err)
	}

	if err := svc.RestartWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake2"}); err != nil {
		t.Fatalf("RestartWorker: %v", err)
	}
	if len(*other) != 1 {
		t.Fatalf("built %d fake2 workers, want 1", len(*other))
	}
	if got := (*other)[0].restored; got != nil {
		t.Fatalf("restored = %q, want no snapshot for a type change", got)
	}
}

// TestRestartWorkerUnknownIDCreates verifies one path serves "restart" and
// "start from declaration" (the WebUI's declared-but-stopped case).
func TestRestartWorkerUnknownIDCreates(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)

	if err := svc.RestartWorker(context.Background(), worker.WorkerConfig{ID: "fresh", Type: "fake"}); err != nil {
		t.Fatalf("RestartWorker: %v", err)
	}
	if len(*built) != 1 || !svc.HasWorker("fresh") {
		t.Fatalf("built = %d, has = %v; want a running fresh worker", len(*built), svc.HasWorker("fresh"))
	}
}

// registerTwoFakeBuilders registers "fake" and "fake2", each recording the
// instances it builds, for the type-change test.
func registerTwoFakeBuilders(svc *WorkerService) (first, second *[]*fakeWorker) {
	f, s := []*fakeWorker{}, []*fakeWorker{}
	svc.RegisterBuilder("fake", fakeBuilderFor(&f))
	svc.RegisterBuilder("fake2", fakeBuilderFor(&s))
	return &f, &s
}

func fakeBuilderFor(built *[]*fakeWorker) Builder {
	return func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		w := &fakeWorker{id: cfg.ID, snap: []byte("snap")}
		*built = append(*built, w)
		return worker.SpawnSpec{
			Config:  cfg,
			Connect: func() (corebus.WorkerSideChannel, error) { return fakeChannel{}, nil },
			Build:   func(corebus.WorkerSideChannel) worker.ManagedWorker { return w },
		}, nil
	}
}
