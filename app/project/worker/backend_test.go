package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	projprocess "github.com/niq-run/niq/app/project/worker/process"
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workerhost"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// memDecls is an in-memory DeclStore: the lifecycle is tested without a project
// file, which is the point of the interface.
type memDecls struct {
	mu   sync.Mutex
	list []*Declaration
}

func (m *memDecls) Get(id string) (*Declaration, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.list {
		if d.ID == id {
			c := *d
			return &c, true, nil
		}
	}
	return nil, false, nil
}

func (m *memDecls) List() ([]*Declaration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Declaration, 0, len(m.list))
	for _, d := range m.list {
		c := *d
		out = append(out, &c)
	}
	return out, nil
}

func (m *memDecls) Add(d *Declaration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.list {
		if x.ID == d.ID {
			return fmt.Errorf("worker %s already exists", d.ID)
		}
	}
	c := *d
	m.list = append(m.list, &c)
	return nil
}

func (m *memDecls) Update(d *Declaration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.list {
		if x.ID == d.ID {
			c := *d
			m.list[i] = &c
			return nil
		}
	}
	return fmt.Errorf("worker %s is not declared", d.ID)
}

func (m *memDecls) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.list {
		if x.ID == id {
			m.list = append(m.list[:i], m.list[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *memDecls) SetCredential(id, cred string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.list {
		if x.ID == id {
			x.Credential = cred
			return nil
		}
	}
	return fmt.Errorf("worker %s is not declared", id)
}

func (m *memDecls) RemoveState(string) error { return nil }

// fakeWorker is the minimal managed worker a test builder produces.
type fakeWorker struct {
	baseworker.BaseWorker
	started bool
}

func (w *fakeWorker) Start(context.Context) error { w.started = true; return nil }
func (w *fakeWorker) Stop() error                 { w.started = false; return nil }
func (w *fakeWorker) Snapshot() ([]byte, error)   { return []byte(`{"started":true}`), nil }
func (w *fakeWorker) Restore([]byte) error        { return nil }

type testRig struct {
	backend    *workerHostBackend
	service    *workerhost.WorkerService
	supervisor *projprocess.ProcessSupervisor
	decls      *memDecls
	registry   corebus.IdentityRegistry
}

// newTestRig wires the backend exactly as the assembly does, over a real bus
// (in-process listener) and a real identity registry, with a fake worker type
// whose builder derives its id — which is what makes the "declared id is the
// resolved id" invariant testable.
func newTestRig(t *testing.T) *testRig {
	t.Helper()
	dir := t.TempDir()

	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "id", "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	layered := eventbus.NewLayeredRegistry(registry)
	engine := eventbus.NewEngine(layered, eventbus.NewMemoryEventStore())
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

	svc := workerhost.New()
	stateStore := newMemStore()

	// A worker type whose builder derives the id from params.name, mirroring
	// how workspace derives an id from its path.
	svc.RegisterBuilder("fake", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		if cfg.ID == "" {
			name, _ := cfg.Params["name"].(string)
			cfg.ID = "derived-" + name
		}
		id := cfg.ID
		connect := func() (corebus.WorkerSideChannel, error) {
			if err := projprocess.RegisterIdentity(layered, corebus.Identity{WorkerID: id, Type: "fake"}); err != nil {
				return nil, err
			}
			ch := inprocess.NewWorkerSide(id, listener)
			if err := ch.Connect(context.Background(), "inproc://test"); err != nil {
				return nil, err
			}
			return ch, nil
		}
		build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
			return &fakeWorker{BaseWorker: baseworker.NewBaseWorker(id, ch)}
		}
		return coreworker.SpawnSpec{Config: cfg, Connect: connect, Build: build}, nil
	})
	svc.RegisterParamCheck("fake", CheckSpawnParams("fake", []string{"name"}))

	decls := &memDecls{}
	sup := projprocess.NewProcessSupervisor("http://127.0.0.1:1", filepath.Join(dir, "workers"), func(string, ...any) {})
	t.Cleanup(sup.Shutdown)

	backend := NewBackend(BackendConfig{
		ProjectID:  "test",
		Service:    svc,
		StateStore: stateStore,
		Supervisor: sup,
		Registry:   layered,
		Signer:     eventbus.NewTokenSigner([]byte("test-secret")),
		Engine:     engine,
		Decls:      decls,
		Logf:       func(string, ...any) {},
	})
	return &testRig{backend: backend, service: svc, supervisor: sup, decls: decls, registry: layered}
}

// TestCreateInProcessDeclaresResolvedID is the regression behind the old
// derived-id bug: the declaration and the reply must carry the id the BUILDER
// produced, not the empty id the caller passed.
func TestCreateInProcessDeclaresResolvedID(t *testing.T) {
	rig := newTestRig(t)
	info, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{Type: "fake", Params: map[string]any{"name": "alpha"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID != "derived-alpha" {
		t.Fatalf("info.ID = %q, want the id the builder derived", info.ID)
	}
	if info.Placement != coreworker.PlacementInProcess || info.State != "running" {
		t.Fatalf("info = %+v", info)
	}
	d, ok, err := rig.decls.Get("derived-alpha")
	if err != nil || !ok {
		t.Fatalf("declaration missing: %v %v", ok, err)
	}
	if d.ID != "derived-alpha" || d.Type != "fake" || !IsInProcess(*d) {
		t.Fatalf("declaration = %+v", d)
	}
}

// TestCreateRejectsUnreadParams verifies the type's spawn contract still guards
// the boundary through the backend.
func TestCreateRejectsUnreadParams(t *testing.T) {
	rig := newTestRig(t)
	_, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake", Params: map[string]any{"prompt": "x"}},
	})
	if err == nil {
		t.Fatal("Create accepted a parameter the type does not read")
	}
	if rig.service.HasWorker("w1") {
		t.Fatal("the rejected create still registered a worker")
	}
	if _, ok, _ := rig.decls.Get("w1"); ok {
		t.Fatal("the rejected create still persisted a declaration")
	}
}

// TestLifecycleInProcess walks create → suspend → resume → restart → destroy
// through the backend, asserting the declaration and the live state stay in
// step.
func TestLifecycleInProcess(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := rig.backend.Suspend(ctx, "w1"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if info, ok, _ := rig.backend.Get("w1"); !ok || info.State != "suspended" {
		t.Fatalf("after suspend: %+v (ok=%v)", info, ok)
	}

	if err := rig.backend.Resume(ctx, "w1"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if info, _, _ := rig.backend.Get("w1"); info.State != "running" {
		t.Fatalf("after resume: %+v", info)
	}

	if err := rig.backend.Restart(ctx, "w1"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if info, _, _ := rig.backend.Get("w1"); info.State != "running" {
		t.Fatalf("after restart: %+v", info)
	}

	if err := rig.backend.Destroy(ctx, "w1"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok, _ := rig.backend.Get("w1"); ok {
		t.Fatal("destroyed worker is still in the catalog")
	}
	if _, ok := rig.registry.Lookup("w1"); ok {
		t.Fatal("destroy did not revoke the bus identity")
	}
}

// TestDestroyRefusesProtected verifies the host itself cannot be removed.
func TestDestroyRefusesProtected(t *testing.T) {
	rig := newTestRig(t)
	if err := rig.backend.Recover(context.Background(), coreworker.RecoverOptions{
		Protect: []string{"host"},
	}); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := rig.backend.Destroy(context.Background(), "host"); err == nil {
		t.Fatal("protected worker was destroyed")
	}
	if err := rig.backend.Suspend(context.Background(), "host"); err == nil {
		t.Fatal("protected worker was suspended")
	}
}

// TestCreateManagedProcess launches a real child process, then stops and
// re-launches it through the lifecycle.
func TestCreateManagedProcess(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	marker := filepath.Join(t.TempDir(), "runs.log")

	info, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "proc", Type: "mcp"},
		Placement:    coreworker.PlacementManagedProcess,
		Command:      []string{"/bin/sh", "-c", "echo run >> " + marker + "; sleep 60"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Placement != coreworker.PlacementManagedProcess || info.State != "running" {
		t.Fatalf("info = %+v", info)
	}

	d, ok, _ := rig.decls.Get("proc")
	if !ok || PlacementOf(*d) != coreworker.PlacementManagedProcess || len(d.Command) == 0 {
		t.Fatalf("declaration = %+v", d)
	}
	// A launched child carries a TEMP credential: it must not be persisted.
	if d.Credential != "" {
		t.Fatalf("temp credential persisted: %q", d.Credential)
	}
	if _, ok := rig.registry.Lookup("proc"); !ok {
		t.Fatal("identity not registered")
	}

	waitForFile(t, marker)

	if err := rig.backend.Suspend(ctx, "proc"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if info, _, _ := rig.backend.Get("proc"); info.State != "stopped" {
		t.Fatalf("after suspend: %+v", info)
	}
	if err := rig.backend.Resume(ctx, "proc"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := rig.backend.Destroy(ctx, "proc"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, ok, _ := rig.decls.Get("proc"); ok {
		t.Fatal("declaration survived destroy")
	}
	if _, ok := rig.registry.Lookup("proc"); ok {
		t.Fatal("identity survived destroy")
	}
}

// TestCreateUnmanagedProcess verifies a dial-in worker is declared and
// provisioned with a PERSISTED credential, and that nothing is launched.
func TestCreateUnmanagedProcess(t *testing.T) {
	rig := newTestRig(t)
	info, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "remote", Type: "mcp"},
		Placement:    coreworker.PlacementUnmanagedProcess,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Placement != coreworker.PlacementUnmanagedProcess {
		t.Fatalf("info = %+v", info)
	}
	d, ok, _ := rig.decls.Get("remote")
	if !ok || PlacementOf(*d) != coreworker.PlacementUnmanagedProcess {
		t.Fatalf("declaration = %+v", d)
	}
	if d.Credential == "" {
		t.Fatal("credential not persisted onto the declaration")
	}
	id, ok := rig.registry.Lookup("remote")
	if !ok || id.Credential != d.Credential || !id.Remote {
		t.Fatalf("registry identity = %+v (ok=%v)", id, ok)
	}
	if sts := rig.supervisor.List(); len(sts) != 0 {
		t.Fatalf("an unmanaged-process worker must not be supervised: %v", sts)
	}
	// It has no local lifecycle.
	if err := rig.backend.Suspend(context.Background(), "remote"); err == nil {
		t.Fatal("unmanaged-process worker was suspended")
	}
	if err := rig.backend.Restart(context.Background(), "remote"); err == nil {
		t.Fatal("unmanaged-process worker was restarted")
	}
}

// TestCreateRequiresCommandForProcess verifies the launch spec is checked before
// anything is persisted.
func TestCreateRequiresCommandForProcess(t *testing.T) {
	rig := newTestRig(t)
	_, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "p", Type: "mcp"},
		Placement:    coreworker.PlacementManagedProcess,
	})
	if err == nil {
		t.Fatal("managed-process create accepted an empty command")
	}
	if _, ok, _ := rig.decls.Get("p"); ok {
		t.Fatal("rejected create still persisted a declaration")
	}
}

// TestCatalogReportsStoppedAndRunning verifies List() covers declared-but-idle
// workers, which is what the start button is rendered from.
func TestCatalogReportsStoppedAndRunning(t *testing.T) {
	rig := newTestRig(t)
	if err := rig.decls.Add(&Declaration{Config: Config{ID: "idle", Type: "fake"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "live", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	states := map[string]string{}
	for _, info := range rig.backend.List() {
		states[info.ID] = info.State
	}
	if states["idle"] != "stopped" {
		t.Fatalf("idle state = %q, want stopped", states["idle"])
	}
	if states["live"] != "running" {
		t.Fatalf("live state = %q, want running", states["live"])
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.ReadFile(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s was never written", path)
}

// TestCreateUnmanagedProcessRejectsParams verifies params are not silently
// ignored: there is no channel to pass them to a self-started worker.
func TestCreateUnmanagedProcessRejectsParams(t *testing.T) {
	rig := newTestRig(t)
	_, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "remote", Type: "mcp", Params: map[string]any{"K": "V"}},
		Placement:    coreworker.PlacementUnmanagedProcess,
	})
	if err == nil {
		t.Fatal("unmanaged-process create accepted params")
	}
	if _, ok, _ := rig.decls.Get("remote"); ok {
		t.Fatal("rejected create persisted a declaration")
	}
}

// TestManagedProcessParamsBecomeEnv verifies a managed-process spec's params are
// lowered into the child environment (and secret references are resolved).
func TestManagedProcessParamsBecomeEnv(t *testing.T) {
	d := Declaration{
		Config:    Config{ID: "p", Type: "probe", Params: map[string]any{"TOKEN": "env:SEP5_TOKEN", "ROOT": "/data"}},
		Placement: coreworker.PlacementManagedProcess,
		Command:   []string{"x"},
	}
	t.Setenv("SEP5_TOKEN", "resolved")
	env := launchSpec(d, "cred").Env
	if env["TOKEN"] != "resolved" || env["ROOT"] != "/data" {
		t.Fatalf("env = %+v", env)
	}
}
