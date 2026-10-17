package workerhost

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/itfs/worker"
)

// TestRestartWorkerRebuilds verifies a restart discards the live instance and
// its persisted snapshot, then spawns a fresh instance from the given config —
// which is what makes a params edit take effect.
func TestRestartWorkerRebuilds(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)
	ctx := context.Background()

	if err := svc.CreateWorker(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "old"}}); err != nil {
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
	if fresh.restored != nil {
		t.Fatalf("fresh worker restored %q, want no snapshot replay", fresh.restored)
	}
	if !fresh.started {
		t.Fatal("fresh worker was not started")
	}
	if got := workerStatus(svc, "w"); got.State != worker.StateRunning {
		t.Fatalf("state = %q, want running", got.State)
	}
	// The old snapshot must not survive: the declaration is authoritative.
	if rec := store.recs["w"]; len(rec.Snapshot) != 0 {
		t.Fatalf("store snapshot = %q, want none after restart", rec.Snapshot)
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
