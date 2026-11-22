package workerctl

import (
	"context"
	"fmt"
	"sync"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
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

func (f *fakeWorker) ID() string                  { return f.id }
func (f *fakeWorker) Start(context.Context) error { f.started = true; return nil }
func (f *fakeWorker) Stop() error                 { return nil }
func (f *fakeWorker) Snapshot() ([]byte, error)   { return f.snap, nil }
func (f *fakeWorker) Restore(state []byte) error  { f.restored = state; return nil }

// memoryStore is an in-memory WorkerStore for tests. It only holds runtime
// state — the definitions come from the caller on recovery.
type memoryStore struct {
	recs map[string]StateRecord
}

func newMemoryStore() *memoryStore { return &memoryStore{recs: map[string]StateRecord{}} }

func (m *memoryStore) SaveState(id string, state worker.WorkerState, snapshot []byte) error {
	rec := m.recs[id]
	rec.ID, rec.State, rec.Snapshot = id, state, snapshot
	m.recs[id] = rec
	return nil
}

func (m *memoryStore) LoadAll() ([]StateRecord, error) {
	var out []StateRecord
	for _, r := range m.recs {
		out = append(out, r)
	}
	return out, nil
}

func (m *memoryStore) Delete(id string) error {
	delete(m.recs, id)
	return nil
}

// testBuilder is the type registry a Manager receives: it maps a type label to
// a builder. Production keeps this table in the worker control plane; here it is
// minimal.
type testBuilder struct {
	mu       sync.Mutex
	builders map[string]Builder
}

func newTestBuilder() *testBuilder {
	return &testBuilder{builders: map[string]Builder{}}
}

func (b *testBuilder) register(typ string, fn Builder) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.builders[typ] = fn
}

func (b *testBuilder) Build(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	b.mu.Lock()
	fn, ok := b.builders[cfg.Type]
	b.mu.Unlock()
	if !ok {
		return worker.SpawnSpec{}, fmt.Errorf("no builder for type %q", cfg.Type)
	}
	return fn(cfg)
}

func newServiceWithStore(t *testing.T, _ string) (*Manager, *testBuilder) {
	t.Helper()
	tb := newTestBuilder()
	svc := New(Config{Store: newMemoryStore(), Build: tb.Build})
	return svc, tb
}

// registerFakeBuilder registers a "fake" builder that appends every built
// worker to the returned slice.
func registerFakeBuilder(tb *testBuilder) *[]*fakeWorker {
	built := []*fakeWorker{}
	tb.register("fake", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
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

func workerStatus(svc *Manager, id string) worker.WorkerInfo {
	for _, wi := range svc.List("") {
		if wi.ID == id {
			return wi
		}
	}
	return worker.WorkerInfo{}
}

// TestRestoreAndRun verifies a persisted snapshot is replayed onto a freshly
// built worker before start, and the worker ends up running and persisted.
func TestRestoreAndRun(t *testing.T) {
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)

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
	if info := workerStatus(svc, "reason-worker"); info.State != string(worker.StateRunning) {
		t.Fatalf("state = %s, want running", info.State)
	}

	// Persisted config + running state should be on disk for a fresh process.
	recs, err := svc.LoadAll()
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
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)
	if err := svc.RestoreAndRun(context.Background(), worker.WorkerConfig{ID: "x", Type: "fake"}, nil); err != nil {
		t.Fatalf("RestoreAndRun: %v", err)
	}
	if (*built)[0].restored != nil {
		t.Fatalf("Restore called with %q for nil snapshot", (*built)[0].restored)
	}
	if info := workerStatus(svc, "x"); info.State != string(worker.StateRunning) {
		t.Fatalf("state = %s, want running", info.State)
	}
}

// TestRecoverAllRespectsPersistedState verifies RecoverAll re-materializes
// every persisted worker per its own state: running → running (snapshot
// replayed), suspended → suspended.
func TestRecoverAllRespectsPersistedState(t *testing.T) {
	store := newMemoryStore()
	tb := newTestBuilder()
	svc := New(Config{Build: tb.Build})
	svc.SetStore(store)
	built := registerFakeBuilder(tb)

	// Only the runtime state is persisted; the declaration is the caller's.
	store.SaveState("run-w", worker.StateRunning, []byte("snap-run"))
	store.SaveState("susp-w", worker.StateSuspended, []byte("snap-susp"))

	declared := []worker.WorkerConfig{
		{ID: "run-w", Type: "fake", Params: map[string]any{"p": 1}},
		{ID: "susp-w", Type: "fake"},
	}
	if err := svc.RecoverAll(context.Background(), declared, nil); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}

	if info := workerStatus(svc, "run-w"); info.State != string(worker.StateRunning) {
		t.Fatalf("run-w state = %s, want running", info.State)
	}
	if info := workerStatus(svc, "susp-w"); info.State != string(worker.StateSuspended) {
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

// TestProtectedWorkersCannotBeSuspendedOrDestroyed verifies ids handed to
// RecoverAll's protect list are protected afterwards.
func TestProtectedWorkersCannotBeSuspendedOrDestroyed(t *testing.T) {
	store := newMemoryStore()
	tb := newTestBuilder()
	svc := New(Config{Build: tb.Build})
	svc.SetStore(store)
	registerFakeBuilder(tb)

	store.SaveState("essential-w", worker.StateRunning, nil)

	if err := svc.RecoverAll(context.Background(),
		[]worker.WorkerConfig{{ID: "essential-w", Type: "fake"}},
		[]string{"essential-w"},
	); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}
	if err := svc.Suspend("essential-w"); err == nil {
		t.Fatal("Suspend on protected worker should fail")
	}
	if err := svc.Destroy("essential-w"); err == nil {
		t.Fatal("Destroy on protected worker should fail")
	}
	// A non-essential worker is not protected.
	store.SaveState("plain", worker.StateRunning, nil)
	if _, err := svc.Create(context.Background(), worker.WorkerConfig{ID: "plain", Type: "fake"}); err != nil {
		t.Fatalf("create plain: %v", err)
	}
	if err := svc.Suspend("plain"); err != nil {
		t.Fatalf("Suspend on non-protected should succeed: %v", err)
	}
}

// TestRestoreSuspendedKeepsSnapshot verifies a suspended worker restored by
// RecoverAll replays its snapshot when later resumed.
func TestRestoreSuspendedKeepsSnapshot(t *testing.T) {
	store := newMemoryStore()
	tb := newTestBuilder()
	svc := New(Config{Build: tb.Build})
	svc.SetStore(store)
	built := registerFakeBuilder(tb)

	store.SaveState("w", worker.StateSuspended, []byte("snap-w"))

	if err := svc.RecoverAll(context.Background(), []worker.WorkerConfig{{ID: "w", Type: "fake"}}, nil); err != nil {
		t.Fatalf("RecoverAll: %v", err)
	}
	if err := svc.Resume(context.Background(), "w"); err != nil {
		t.Fatalf("Resume: %v", err)
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
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)

	cfg := worker.WorkerConfig{ID: "reason-worker", Type: "fake"}
	if _, err := svc.Create(context.Background(), cfg); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The worker's state changed after it was created (e.g. a provider switch).
	(*built)[0].snap = []byte("after-switch")

	if err := svc.Checkpoint("reason-worker"); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}

	recs, err := svc.LoadAll()
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
	svc, tb := newServiceWithStore(t, t.TempDir())
	registerFakeBuilder(tb)

	if _, err := svc.Create(context.Background(), worker.WorkerConfig{ID: "w", Type: "fake"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Suspend("w"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if err := svc.Checkpoint("w"); err == nil {
		t.Fatal("Checkpoint on a suspended worker should fail")
	}
	if err := svc.Checkpoint("nope"); err == nil {
		t.Fatal("Checkpoint on an unknown worker should fail")
	}
}

// TestRestartWorkerKeepsState verifies a restart replays the persisted snapshot
// onto the freshly built instance: construction inputs come from the new config,
// state the worker already owns survives. That is the same sequence recovery and
// resume use, so every way of bringing a worker up means one thing.
func TestRestartWorkerKeepsState(t *testing.T) {
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)
	ctx := context.Background()

	if _, err := svc.Create(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "old"}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Suspend so the store holds a snapshot and the worker is not running: a
	// restart must still replace it (suspended entries are restartable).
	if err := svc.Suspend("w"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	store := svc.store.(*memoryStore)
	if rec := store.recs["w"]; len(rec.Snapshot) == 0 || rec.State != worker.StateSuspended {
		t.Fatalf("store after suspend = %+v, want a suspended snapshot", rec)
	}

	if err := svc.Restart(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "new"}}); err != nil {
		t.Fatalf("Restart: %v", err)
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
	if got := workerStatus(svc, "w"); got.State != string(worker.StateRunning) {
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
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)
	ctx := context.Background()

	if _, err := svc.Create(ctx, worker.WorkerConfig{ID: "w", Type: "fake"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Newer state than the store holds (spawn persisted no snapshot).
	(*built)[0].snap = []byte("learned-since-start")

	if err := svc.Restart(ctx, worker.WorkerConfig{ID: "w", Type: "fake", Params: map[string]any{"v": "new"}}); err != nil {
		t.Fatalf("Restart: %v", err)
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
	svc, tb := newServiceWithStore(t, t.TempDir())
	_, other := registerTwoFakeBuilders(tb)
	ctx := context.Background()

	if _, err := svc.Create(ctx, worker.WorkerConfig{ID: "w", Type: "fake"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := svc.Suspend("w"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}

	if err := svc.Restart(ctx, worker.WorkerConfig{ID: "w", Type: "fake2"}); err != nil {
		t.Fatalf("Restart: %v", err)
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
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)

	if err := svc.Restart(context.Background(), worker.WorkerConfig{ID: "fresh", Type: "fake"}); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if len(*built) != 1 || !svc.Has("fresh") {
		t.Fatalf("built = %d, has = %v; want a running fresh worker", len(*built), svc.Has("fresh"))
	}
}

// registerTwoFakeBuilders registers "fake" and "fake2", each recording the
// instances it builds, for the type-change test.
func registerTwoFakeBuilders(tb *testBuilder) (first, second *[]*fakeWorker) {
	f, s := []*fakeWorker{}, []*fakeWorker{}
	tb.register("fake", fakeBuilderFor(&f))
	tb.register("fake2", fakeBuilderFor(&s))
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

// TestCreateWorkerRollsBackWhenSpawnHookFails verifies a failed declaration
// does not leave a live, undeclared worker behind, and that the resolved
// config is still returned so the caller can diagnose it.
func TestCreateWorkerRollsBackWhenSpawnHookFails(t *testing.T) {
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)
	svc.SetSpawnHook(func(worker.WorkerConfig) error { return fmt.Errorf("disk full") })

	resolved, err := svc.Create(context.Background(), worker.WorkerConfig{ID: "rollback", Type: "fake"})
	if err == nil {
		t.Fatal("Create succeeded despite spawn-hook failure")
	}
	if resolved.ID != "rollback" {
		t.Fatalf("resolved id = %q, want rollback", resolved.ID)
	}
	if svc.Has("rollback") {
		t.Fatal("worker is still registered after rollback")
	}
	if _, ok := svc.store.(*memoryStore).recs["rollback"]; ok {
		t.Fatal("state was persisted for a rolled-back worker")
	}
	if len(*built) != 1 {
		t.Fatalf("built = %d, want 1", len(*built))
	}
}

// TestCreateWorkerDuplicateDoesNotBuildOrStartAVisibleWorker verifies the
// duplicate check happens before a second instance is exposed.
func TestCreateWorkerDuplicateDoesNotBuildOrStartAVisibleWorker(t *testing.T) {
	svc, tb := newServiceWithStore(t, t.TempDir())
	built := registerFakeBuilder(tb)

	if _, err := svc.Create(context.Background(), worker.WorkerConfig{ID: "dup", Type: "fake"}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := svc.Create(context.Background(), worker.WorkerConfig{ID: "dup", Type: "fake"}); err == nil {
		t.Fatal("second Create succeeded for a duplicate id")
	}
	if got := len(svc.List("")); got != 1 {
		t.Fatalf("workers = %d, want 1", got)
	}
	_ = built
}
