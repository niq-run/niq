package workerhost

import (
	"context"
	"testing"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/worker"
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
	snap     []byte // what Snapshot() returns, so a test can see a fresh read
}

func (f *fakeWorker) ID() string                          { return f.id }
func (f *fakeWorker) Start(context.Context) error         { f.started = true; return nil }
func (f *fakeWorker) Subscriptions() []event.EventPattern { return nil }
func (f *fakeWorker) Stop() error                         { return nil }
func (f *fakeWorker) Snapshot() ([]byte, error)           { return f.snap, nil }
func (f *fakeWorker) Restore(state []byte) error          { f.restored = state; return nil }

// memoryStore is an in-memory WorkerStore for tests.
type memoryStore struct {
	recs map[string]WorkerRecord
}

func newMemoryStore() *memoryStore { return &memoryStore{recs: map[string]WorkerRecord{}} }

func (m *memoryStore) SaveConfig(cfg worker.WorkerConfig) error {
	rec := m.recs[cfg.ID]
	rec.ID, rec.Type, rec.Params = cfg.ID, cfg.Type, cfg.Params
	m.recs[cfg.ID] = rec
	return nil
}

func (m *memoryStore) SaveState(id string, state worker.WorkerState, snapshot []byte) error {
	rec := m.recs[id]
	rec.State, rec.Snapshot = state, snapshot
	m.recs[id] = rec
	return nil
}

func (m *memoryStore) LoadAll() ([]WorkerRecord, error) {
	var out []WorkerRecord
	for _, r := range m.recs {
		out = append(out, r)
	}
	return out, nil
}

func (m *memoryStore) Delete(id string) error {
	delete(m.recs, id)
	return nil
}

func newServiceWithStore(t *testing.T, _ string) *WorkerService {
	t.Helper()
	svc := New()
	svc.SetStore(newMemoryStore())
	return svc
}

// registerFakeBuilder registers a "fake" builder that appends every built
// worker to the returned slice.
func registerFakeBuilder(svc *WorkerService) *[]*fakeWorker {
	built := []*fakeWorker{}
	svc.RegisterBuilder("fake", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		w := &fakeWorker{id: cfg.ID, snap: []byte("snap")}
		built = append(built, w)
		return worker.SpawnSpec{
			Config:  cfg,
			Connect: func() (corebus.WorkerSideChannel, error) { return fakeChannel{}, nil },
			Build:   func(corebus.WorkerSideChannel) worker.ManagedWorker { return w },
		}, nil
	})
	return &built
}

func workerStatus(svc *WorkerService, id string) WorkerInfo {
	for _, wi := range svc.ListWorkers("") {
		if wi.ID == id {
			return wi
		}
	}
	return WorkerInfo{}
}

// TestRestoreAndRun verifies a persisted snapshot is replayed onto a freshly
// built worker before start, and the worker ends up running and persisted.
func TestRestoreAndRun(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)

	snapshot := []byte("the-persisted-transcript")
	cfg := worker.WorkerConfig{ID: "reason-worker", Type: "fake", Params: map[string]any{"p": 1}}
	if err := svc.RestoreAndRun(context.Background(), cfg, snapshot); err != nil {
		t.Fatalf("RestoreAndRun: %v", err)
	}

	if len(*built) != 1 || (*built)[0].id != "reason-worker" {
		t.Fatalf("built = %+v, want reason-worker", *built)
	}
	if !(*built)[0].started {
		t.Fatal("worker not started")
	}
	if string((*built)[0].restored) != string(snapshot) {
		t.Fatalf("restored = %q, want %q", (*built)[0].restored, snapshot)
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
	built := registerFakeBuilder(svc)
	if err := svc.RestoreAndRun(context.Background(), worker.WorkerConfig{ID: "x", Type: "fake"}, nil); err != nil {
		t.Fatalf("RestoreAndRun: %v", err)
	}
	if (*built)[0].restored != nil {
		t.Fatalf("Restore called with %q for nil snapshot", (*built)[0].restored)
	}
	if info := workerStatus(svc, "x"); info.State != worker.StateRunning {
		t.Fatalf("state = %s, want running", info.State)
	}
}

// TestRecoverAllRespectsPersistedState verifies RecoverAll re-materializes
// every persisted worker per its own state: running → running (snapshot
// replayed), suspended → suspended.
func TestRecoverAllRespectsPersistedState(t *testing.T) {
	store := newMemoryStore()
	svc := New()
	svc.SetStore(store)
	built := registerFakeBuilder(svc)

	store.SaveConfig(worker.WorkerConfig{ID: "run-w", Type: "fake", Params: map[string]any{"p": 1}})
	store.SaveState("run-w", worker.StateRunning, []byte("snap-run"))
	store.SaveConfig(worker.WorkerConfig{ID: "susp-w", Type: "fake"})
	store.SaveState("susp-w", worker.StateSuspended, []byte("snap-susp"))

	if err := svc.RecoverAll(context.Background(), RecoverOptions{}); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}

	if info := workerStatus(svc, "run-w"); info.State != worker.StateRunning {
		t.Fatalf("run-w state = %s, want running", info.State)
	}
	if info := workerStatus(svc, "susp-w"); info.State != worker.StateSuspended {
		t.Fatalf("susp-w state = %s, want suspended", info.State)
	}
	// The running worker was restored with its snapshot; the suspended one was
	// not started at all.
	for _, w := range *built {
		if w.id == "run-w" && string(w.restored) != "snap-run" {
			t.Fatalf("run-w restored = %q, want snap-run", w.restored)
		}
		if w.id == "susp-w" && w.started {
			t.Fatal("susp-w should not be started by recovery")
		}
	}
}

// TestRecoverAllEssentialAndSuspend verifies the Essential/Suspend overrides:
// essential workers are created when absent and force-run when suspended; ids
// in Suspend are suspended even when persisted running.
func TestRecoverAllEssentialAndSuspend(t *testing.T) {
	store := newMemoryStore()
	svc := New()
	svc.SetStore(store)
	_ = registerFakeBuilder(svc)

	// webui-hiw persisted as suspended (defensive) + a worker persisted running.
	store.SaveConfig(worker.WorkerConfig{ID: "webui-hiw", Type: "fake"})
	store.SaveState("webui-hiw", worker.StateSuspended, []byte("snap-hiw"))
	store.SaveConfig(worker.WorkerConfig{ID: "idle", Type: "fake"})
	store.SaveState("idle", worker.StateRunning, []byte("snap-idle"))

	opts := RecoverOptions{
		Essential: []worker.WorkerConfig{{ID: "webui-hiw", Type: "fake"}, {ID: "absent-essential", Type: "fake"}},
		Suspend:   []string{"idle"},
	}
	if err := svc.RecoverAll(context.Background(), opts); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}

	// Essential suspended → force-run; essential absent → created running.
	if info := workerStatus(svc, "webui-hiw"); info.State != worker.StateRunning {
		t.Fatalf("webui-hiw state = %s, want running (essential force-run)", info.State)
	}
	if info := workerStatus(svc, "absent-essential"); info.State != worker.StateRunning {
		t.Fatalf("absent-essential state = %s, want running", info.State)
	}
	// Suspend set wins over persisted running.
	if info := workerStatus(svc, "idle"); info.State != worker.StateSuspended {
		t.Fatalf("idle state = %s, want suspended", info.State)
	}
}

// TestProtectedWorkersCannotBeSuspendedOrDestroyed verifies essential ids are
// protected after RecoverAll.
func TestProtectedWorkersCannotBeSuspendedOrDestroyed(t *testing.T) {
	store := newMemoryStore()
	svc := New()
	svc.SetStore(store)
	registerFakeBuilder(svc)

	store.SaveConfig(worker.WorkerConfig{ID: "essential-w", Type: "fake"})
	store.SaveState("essential-w", worker.StateRunning, nil)

	if err := svc.RecoverAll(context.Background(), RecoverOptions{
		Essential: []worker.WorkerConfig{{ID: "essential-w", Type: "fake"}},
	}); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}
	if err := svc.SuspendWorker("essential-w"); err == nil {
		t.Fatal("SuspendWorker on protected worker should fail")
	}
	if err := svc.DestroyWorker("essential-w"); err == nil {
		t.Fatal("DestroyWorker on protected worker should fail")
	}
	// A non-essential worker is not protected.
	store.SaveConfig(worker.WorkerConfig{ID: "plain", Type: "fake"})
	store.SaveState("plain", worker.StateRunning, nil)
	if err := svc.CreateWorker(context.Background(), worker.WorkerConfig{ID: "plain", Type: "fake"}); err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if err := svc.SuspendWorker("plain"); err != nil {
		t.Fatalf("SuspendWorker on non-protected should succeed: %v", err)
	}
}

// TestRestoreSuspendedKeepsSnapshot verifies a suspended worker restored by
// RecoverAll replays its snapshot when later resumed.
func TestRestoreSuspendedKeepsSnapshot(t *testing.T) {
	store := newMemoryStore()
	svc := New()
	svc.SetStore(store)
	built := registerFakeBuilder(svc)

	store.SaveConfig(worker.WorkerConfig{ID: "w", Type: "fake"})
	store.SaveState("w", worker.StateSuspended, []byte("snap-w"))

	if err := svc.RecoverAll(context.Background(), RecoverOptions{}); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}
	if err := svc.ResumeWorker(context.Background(), "w"); err != nil {
		t.Fatalf("ResumeWorker: %v", err)
	}
	for _, w := range *built {
		if w.id == "w" && string(w.restored) != "snap-w" {
			t.Fatalf("resumed worker restored = %q, want snap-w", w.restored)
		}
	}
}

// TestCheckpointPersistsRunningState verifies Checkpoint writes the worker's
// current state to the store — the path a runtime provider switch takes so it
// survives a crash rather than waiting for suspend/shutdown.
func TestCheckpointPersistsRunningState(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(svc)

	cfg := worker.WorkerConfig{ID: "reason-worker", Type: "fake"}
	if err := svc.CreateWorker(context.Background(), cfg); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	// The worker's state changed after it was created (e.g. a provider switch).
	(*built)[0].snap = []byte("after-switch")

	if err := svc.Checkpoint("reason-worker"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	recs, err := svc.LoadAllWorkers()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("persisted = %+v, want 1 record", recs)
	}
	if string(recs[0].Snapshot) != "after-switch" {
		t.Fatalf("snapshot = %q, want after-switch", recs[0].Snapshot)
	}
	if recs[0].State != worker.StateRunning {
		t.Fatalf("state = %s, want running", recs[0].State)
	}
}

// TestCheckpointRejectsNonRunning verifies Checkpoint refuses workers it cannot
// snapshot: unknown ids and suspended ones (whose state is already persisted).
func TestCheckpointRejectsNonRunning(t *testing.T) {
	svc := newServiceWithStore(t, t.TempDir())
	registerFakeBuilder(svc)

	if err := svc.CreateWorker(context.Background(), worker.WorkerConfig{ID: "w", Type: "fake"}); err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	if err := svc.SuspendWorker("w"); err != nil {
		t.Fatalf("SuspendWorker: %v", err)
	}
	if err := svc.Checkpoint("w"); err == nil {
		t.Fatal("Checkpoint on a suspended worker should fail")
	}
	if err := svc.Checkpoint("nope"); err == nil {
		t.Fatal("Checkpoint on an unknown worker should fail")
	}
}
