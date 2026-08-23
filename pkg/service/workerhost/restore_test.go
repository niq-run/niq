package workerhost

import (
	"context"
	"testing"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/worker"
)

// fakeChannel is a minimal WorkerSideChannel stub; the worker under test never
// actually talks on it while being constructed/restored.
type fakeChannel struct{}

func (fakeChannel) ID() string                                          { return "fake" }
func (fakeChannel) Send(context.Context, event.Event, ...string) error  { return nil }
func (fakeChannel) Broadcast(context.Context, event.Event) error        { return nil }
func (fakeChannel) Receive(context.Context) (<-chan event.Event, error) { return nil, nil }
func (fakeChannel) Close() error                                        { return nil }

// fakeWorker records what Restore received and whether it started.
type fakeWorker struct {
	id       string
	restored []byte
	started  bool
}

func (f *fakeWorker) ID() string                          { return f.id }
func (f *fakeWorker) Start(context.Context) error         { f.started = true; return nil }
func (f *fakeWorker) Subscriptions() []event.EventPattern { return nil }
func (f *fakeWorker) Stop() error                         { return nil }
func (f *fakeWorker) Snapshot() ([]byte, error)           { return []byte("snap"), nil }
func (f *fakeWorker) Restore(state []byte) error          { f.restored = state; return nil }

// TestRestoreAndRun verifies a persisted snapshot is replayed onto a freshly
// built worker before start, and the worker ends up running and persisted.
func TestRestoreAndRun(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	var built *fakeWorker

	svc.RegisterBuilder("fake", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		built = &fakeWorker{id: cfg.ID, restored: nil}
		return worker.SpawnSpec{
			Config: cfg,
			Connect: func() (corebus.WorkerSideChannel, error) {
				return fakeChannel{}, nil
			},
			Build: func(corebus.WorkerSideChannel) worker.ManagedWorker { return built },
		}, nil
	})

	snapshot := []byte("the-persisted-transcript")
	cfg := worker.WorkerConfig{ID: "reason-worker", Type: "fake", Params: map[string]any{"p": 1}}
	if err := svc.RestoreAndRun(context.Background(), cfg, snapshot); err != nil {
		t.Fatalf("RestoreAndRun: %v", err)
	}

	if built == nil || built.id != "reason-worker" {
		t.Fatalf("built = %+v, want reason-worker", built)
	}
	if !built.started {
		t.Fatal("worker not started")
	}
	if string(built.restored) != string(snapshot) {
		t.Fatalf("restored = %q, want %q", built.restored, snapshot)
	}
	if info := workerStatus(svc, "reason-worker"); info.State != worker.StateRunning {
		t.Fatalf("state = %s, want running", info.State)
	}

	// Persisted config + running state should be on disk for a fresh process.
	recs, err := svc.LoadAllWorkers()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "reason-worker" {
		t.Fatalf("persisted = %+v, want 1 reason-worker", recs)
	}
}

// TestRestoreAndRunFreshNoSnapshotSkipsRestore verifies a nil/empty snapshot is
// tolerated (no Restore call) — equivalent to starting a stateless worker.
func TestRestoreAndRunNoSnapshot(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	var built *fakeWorker
	svc.RegisterBuilder("fake", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		built = &fakeWorker{id: cfg.ID}
		return worker.SpawnSpec{
			Config:  cfg,
			Connect: func() (corebus.WorkerSideChannel, error) { return fakeChannel{}, nil },
			Build:   func(corebus.WorkerSideChannel) worker.ManagedWorker { return built },
		}, nil
	})
	if err := svc.RestoreAndRun(context.Background(), worker.WorkerConfig{ID: "x", Type: "fake"}, nil); err != nil {
		t.Fatalf("RestoreAndRun: %v", err)
	}
	if built.restored != nil {
		t.Fatalf("Restore called with %q for nil snapshot", built.restored)
	}
	if info := workerStatus(svc, "x"); info.State != worker.StateRunning {
		t.Fatalf("state = %s, want running", info.State)
	}
}

func newServiceWithStore(t *testing.T, storeDir string) *WorkerService {
	t.Helper()
	svc := New()
	store, err := NewFileWorkerStore(storeDir)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	svc.SetStore(store)
	return svc
}

func workerStatus(svc *WorkerService, id string) WorkerInfo {
	for _, wi := range svc.ListWorkers("") {
		if wi.ID == id {
			return wi
		}
	}
	return WorkerInfo{}
}
