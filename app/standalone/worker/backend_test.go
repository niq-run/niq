package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	projprocess "github.com/niq-run/niq/app/standalone/worker/process"
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// memManifests is an in-memory ManifestStore: the control plane is tested
// without a project file, which is the point of the interface.
type memManifests struct {
	mu       sync.Mutex
	list     []*Manifest
	archived []Manifest
}

func (m *memManifests) Get(id string) (*Manifest, bool, error) {
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

func (m *memManifests) List(archived bool) ([]*Manifest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if archived {
		out := make([]*Manifest, 0, len(m.archived))
		for i := range m.archived {
			c := m.archived[i]
			out = append(out, &c)
		}
		return out, nil
	}
	out := make([]*Manifest, 0, len(m.list))
	for _, d := range m.list {
		c := *d
		out = append(out, &c)
	}
	return out, nil
}

func (m *memManifests) Add(d *Manifest) error {
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

func (m *memManifests) Update(d *Manifest) error {
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

func (m *memManifests) Archive(id string, archived bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if archived {
		for i := range m.list {
			if m.list[i].ID == id {
				m.archived = append(m.archived, *m.list[i])
				m.list = append(m.list[:i], m.list[i+1:]...)
				return nil
			}
		}
		return nil
	}
	for i := range m.archived {
		if m.archived[i].ID == id {
			d := m.archived[i]
			m.list = append(m.list, &d)
			m.archived = append(m.archived[:i], m.archived[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *memManifests) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, x := range m.list {
		if x.ID == id {
			m.list = append(m.list[:i], m.list[i+1:]...)
			return nil
		}
	}
	for i := range m.archived {
		if m.archived[i].ID == id {
			m.archived = append(m.archived[:i], m.archived[i+1:]...)
			return nil
		}
	}
	return nil
}

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
	cp         *ControlPlane
	backend    *workerHostBackend
	manager    *workerctl.Manager
	supervisor *projprocess.Supervisor
	manifests  *memManifests
	registry   corebus.IdentityRegistry
	bus        corebus.Control
}

// newTestRig wires the control plane exactly as the assembly does, over a real
// bus (in-process listener), a real identity registry and the bus control
// plane, with a fake worker type whose builder derives its id — which is what
// makes the "declared id is the resolved id" invariant testable.
func newTestRig(t *testing.T, protect ...string) *testRig {
	t.Helper()
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

	bus := busctl.New(newMemIdentityStore(), registry)
	manifests := &memManifests{}
	sup := projprocess.NewSupervisor("http://127.0.0.1:1", filepath.Join(dir, "workers"), func(string, ...any) {})
	t.Cleanup(sup.Shutdown)

	cp := New(Config{
		ProjectID:  "test",
		Manifests:  manifests,
		Registry:   registry,
		Bus:        bus,
		Signer:     eventbus.NewTokenSigner([]byte("test-secret")),
		Engine:     engine,
		Supervisor: sup,
		Protect:    protect,
		Logf:       func(string, ...any) {},
	})

	// A worker type whose builder derives the id from params.name, mirroring
	// how workspace derives an id from its path.
	cp.RegisterBuilder("fake", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		if cfg.ID == "" {
			name, _ := cfg.Params["name"].(string)
			cfg.ID = "derived-" + name
		}
		id := cfg.ID
		connect := func() (corebus.WorkerSideChannel, error) {
			if err := ensureIdentity(bus, corebus.Identity{WorkerID: id, Type: "fake"}); err != nil {
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
	}, CheckSpawnParams("fake", []string{"name"}))

	return &testRig{cp: cp, backend: cp.backend, manager: cp.Manager(), supervisor: sup, manifests: manifests, registry: registry, bus: bus}
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
	d, ok, err := rig.manifests.Get("derived-alpha")
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
	if rig.manager.Has("w1") {
		t.Fatal("the rejected create still registered a worker")
	}
	if _, ok, _ := rig.manifests.Get("w1"); ok {
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
	rig := newTestRig(t, "host")
	if err := rig.cp.Recover(context.Background()); err != nil {
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

	d, ok, _ := rig.manifests.Get("proc")
	if !ok || PlacementOf(*d) != coreworker.PlacementManagedProcess || len(d.Command) == 0 {
		t.Fatalf("declaration = %+v", d)
	}
	// A launched child carries a TEMP credential: the durable identity keeps
	// the grants but no credential.
	durable, ok := rig.bus.Lookup("proc")
	if !ok || durable.Credential != "" {
		t.Fatalf("durable identity = %+v (ok=%v): temp credential must not be persisted", durable, ok)
	}
	if id, ok := rig.registry.Lookup("proc"); !ok || id.Credential == "" {
		t.Fatalf("runtime identity = %+v (ok=%v): the child needs its temp credential", id, ok)
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
	if _, ok, _ := rig.manifests.Get("proc"); ok {
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
	d, ok, _ := rig.manifests.Get("remote")
	if !ok || PlacementOf(*d) != coreworker.PlacementUnmanagedProcess {
		t.Fatalf("declaration = %+v", d)
	}
	durable, ok := rig.bus.Lookup("remote")
	if !ok || durable.Credential == "" || !durable.Remote {
		t.Fatalf("durable identity = %+v (ok=%v)", durable, ok)
	}
	id, ok := rig.registry.Lookup("remote")
	if !ok || id.Credential != durable.Credential || !id.Remote {
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
	if _, ok, _ := rig.manifests.Get("p"); ok {
		t.Fatal("rejected create still persisted a declaration")
	}
}

// TestListReportsStoppedAndRunning verifies List() covers declared-but-idle
// workers, which is what the start button is rendered from.
func TestListReportsStoppedAndRunning(t *testing.T) {
	rig := newTestRig(t)
	if err := rig.manifests.Add(&Manifest{Definition: Definition{ID: "idle", Type: "fake"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "live", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	states := map[string]string{}
	for _, info := range rig.backend.List(false) {
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
	if _, ok, _ := rig.manifests.Get("remote"); ok {
		t.Fatal("rejected create persisted a declaration")
	}
}

// TestManagedProcessParamsBecomeEnv verifies a managed-process spec's params are
// lowered into the child environment (and secret references are resolved).
func TestManagedProcessParamsBecomeEnv(t *testing.T) {
	d := Manifest{
		Definition: Definition{ID: "p", Type: "probe", Params: map[string]any{"TOKEN": "env:SEP5_TOKEN", "ROOT": "/data"}},
		Placement:  coreworker.PlacementManagedProcess,
		Command:    []string{"x"},
	}
	t.Setenv("SEP5_TOKEN", "resolved")
	env := launchSpec(d, "cred").Env
	if env["TOKEN"] != "resolved" || env["ROOT"] != "/data" {
		t.Fatalf("env = %+v", env)
	}
}

// TestCreateValidatesIdentityAndMeta verifies the checks that used to live in the
// WebUI's declaration creator: an explicit id must be path/identity safe for
// every placement, and display metadata is validated for every placement too.
func TestCreateValidatesIdentityAndMeta(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	long := strings.Repeat("x", 65)
	for _, tt := range []struct {
		name string
		spec coreworker.WorkerSpec
		want string
	}{
		{"missing type", coreworker.WorkerSpec{}, "type is required"},
		{"unsafe in-process id", coreworker.WorkerSpec{
			WorkerConfig: coreworker.WorkerConfig{ID: "a b", Type: "fake"},
		}, "may only contain"},
		{"unsafe process id", coreworker.WorkerSpec{
			WorkerConfig: coreworker.WorkerConfig{ID: "a/b", Type: "mcp"},
			Placement:    coreworker.PlacementUnmanagedProcess,
		}, "may only contain"},
		{"missing process id", coreworker.WorkerSpec{
			WorkerConfig: coreworker.WorkerConfig{Type: "mcp"},
			Placement:    coreworker.PlacementUnmanagedProcess,
		}, "id is required"},
		{"bad label", coreworker.WorkerSpec{
			WorkerConfig: coreworker.WorkerConfig{ID: "r1", Type: "fake", Label: long},
		}, "label"},
	} {
		if _, err := rig.backend.Create(ctx, tt.spec); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
		}
	}
}

// TestUpdateEditsDeclarationOnly verifies Update rewrites the declaration (meta +
// params), validates the RESULT, and leaves the live worker untouched.
func TestUpdateEditsDeclarationOnly(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake", Params: map[string]any{"name": "a"}},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before, _, _ := rig.backend.Get("w1")

	label := "值班"
	info, err := rig.backend.Update(ctx, "w1", coreworker.WorkerPatch{
		Label:  &label,
		Tags:   []string{"ops"},
		Params: map[string]any{"name": "b"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if info.State != before.State {
		t.Fatalf("state changed by an edit: %s → %s", before.State, info.State)
	}
	d, ok, _ := rig.manifests.Get("w1")
	if !ok || d.Label != "值班" || len(d.Tags) != 1 || d.Params["name"] != "b" {
		t.Fatalf("declaration = %+v", d)
	}

	// An omitted field is unchanged; an empty params object clears it.
	desc := "note"
	if _, err := rig.backend.Update(ctx, "w1", coreworker.WorkerPatch{Description: &desc}); err != nil {
		t.Fatalf("Update description: %v", err)
	}
	d, _, _ = rig.manifests.Get("w1")
	if d.Label != "值班" || d.Description != "note" || d.Params["name"] != "b" {
		t.Fatalf("partial edit lost a field: %+v", d)
	}
	if _, err := rig.backend.Update(ctx, "w1", coreworker.WorkerPatch{Params: map[string]any{}}); err != nil {
		t.Fatalf("Update clear params: %v", err)
	}
	if d, _, _ = rig.manifests.Get("w1"); d.Params != nil {
		t.Fatalf("params = %+v, want nil", d.Params)
	}

	// Validation runs on the result: an invalid label is refused and nothing is
	// written.
	bad := " padded"
	if _, err := rig.backend.Update(ctx, "w1", coreworker.WorkerPatch{Label: &bad}); err == nil {
		t.Fatal("invalid label accepted")
	}
	if d, _, _ = rig.manifests.Get("w1"); d.Label != "值班" {
		t.Fatalf("rejected edit changed the declaration: %+v", d)
	}

	// An unknown id is an error, not a silent create.
	if _, err := rig.backend.Update(ctx, "nope", coreworker.WorkerPatch{}); err == nil {
		t.Fatal("updating an undeclared worker succeeded")
	}
}

// TestUpdateRejectsUnreadParams verifies the type's param contract still applies
// to an edit: a key the type does not read cannot be written into its declaration.
func TestUpdateRejectsUnreadParams(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err := rig.backend.Update(ctx, "w1", coreworker.WorkerPatch{Params: map[string]any{"typo": 1}})
	if err == nil || !strings.Contains(err.Error(), "typo") {
		t.Fatalf("err = %v, want the param contract to reject %q", err, "typo")
	}
}

// TestArchiveRoundTrip verifies archive stops a live worker, moves the
// manifest, drops the RUNTIME identity but keeps the durable grants, and that
// unarchive restores and starts it with those grants.
func TestArchiveRoundTrip(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := rig.registry.Lookup("w1"); !ok {
		t.Fatal("a live worker should hold a bus identity")
	}

	if err := rig.backend.Archive(ctx, "w1", true); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if _, ok, _ := rig.manifests.Get("w1"); ok {
		t.Fatal("declaration still active after archive")
	}
	if got, _ := rig.manifests.List(true); len(got) != 1 || got[0].ID != "w1" {
		t.Fatalf("archived = %v", got)
	}
	if got := rig.backend.List(true); len(got) != 1 || got[0].ID != "w1" {
		t.Fatalf("catalog archived = %v", got)
	}
	if _, ok := rig.registry.Lookup("w1"); ok {
		t.Fatal("runtime identity survived archive")
	}
	if _, ok := rig.bus.Lookup("w1"); !ok {
		t.Fatal("durable identity must survive archive so a restore keeps its grants")
	}
	if info, ok, _ := rig.backend.Get("w1"); ok {
		t.Fatalf("archived worker still in the active catalog: %+v", info)
	}
	// Catalog.List must not leak it either: archiving keeps a suspended entry (so a
	// restore can resume its snapshot), and that entry must stay invisible.
	for _, info := range rig.backend.List(false) {
		if info.ID == "w1" {
			t.Fatalf("archived worker still listed: %+v", info)
		}
	}

	if err := rig.backend.Archive(ctx, "w1", false); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if _, ok, _ := rig.manifests.Get("w1"); !ok {
		t.Fatal("declaration not restored")
	}
	if info, ok, _ := rig.backend.Get("w1"); !ok || info.State != "running" {
		t.Fatalf("restored worker = %+v, ok=%v; want running", info, ok)
	}
	if _, ok := rig.registry.Lookup("w1"); !ok {
		t.Fatal("unarchive did not re-project the runtime identity")
	}
}

// TestArchiveRefusesProtected verifies the protection that stops the host or the
// WebUI's HIW from being hidden.
func TestArchiveRefusesProtected(t *testing.T) {
	rig := newTestRig(t, "w1")
	if err := rig.cp.Recover(context.Background()); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if err := rig.backend.Archive(context.Background(), "w1", true); err == nil {
		t.Fatal("archiving a protected worker succeeded")
	}
}

// TestArchiveIsIdempotent verifies a repeated archive/restore is a no-op instead
// of an error (or, worse, a relaunch of a worker that is already running).
func TestArchiveIsIdempotent(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := rig.backend.Archive(ctx, "w1", false); err != nil {
		t.Fatalf("archive=false on an active worker: %v", err)
	}
	if err := rig.backend.Archive(ctx, "w1", true); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if err := rig.backend.Archive(ctx, "w1", true); err != nil {
		t.Fatalf("repeat Archive: %v", err)
	}
	if err := rig.backend.Archive(ctx, "w1", false); err != nil {
		t.Fatalf("Unarchive: %v", err)
	}
	if err := rig.backend.Archive(ctx, "w1", false); err != nil {
		t.Fatalf("repeat Unarchive: %v", err)
	}
	if err := rig.backend.Archive(ctx, "ghost", false); err == nil {
		t.Fatal("restoring an unknown worker succeeded")
	}
}

// TestCreateResolvesProjectPlaceholder verifies the declaration layer resolves
// "{project}" for every create path, so a worker made from a UI form ends up with
// the same fully-resolved params a template-seeded worker has.
func TestCreateResolvesProjectPlaceholder(t *testing.T) {
	rig := newTestRig(t)
	if _, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake", Params: map[string]any{"name": "{project}-ws"}},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	d, _, _ := rig.manifests.Get("w1")
	if d.Params["name"] != "test-ws" {
		t.Fatalf("params = %+v, want the project id substituted", d.Params)
	}
}

// TestListSplitsActiveAndArchived verifies the single read view carries the
// display metadata for the active set, and that the archived set is a separate
// List(true) with Archived=true — no second declaration view.
func TestListSplitsActiveAndArchived(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w1", Type: "fake", Params: map[string]any{"name": "a"}, Label: "值班"},
	}); err != nil {
		t.Fatalf("Create w1: %v", err)
	}
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w2", Type: "fake"},
	}); err != nil {
		t.Fatalf("Create w2: %v", err)
	}
	active := rig.backend.List(false)
	if len(active) != 2 {
		t.Fatalf("active = %+v", active)
	}
	for _, info := range active {
		if info.Archived {
			t.Fatalf("active entry marked archived: %+v", info)
		}
		if info.ID == "w1" && (info.Label != "值班" || info.Params["name"] != "a") {
			t.Fatalf("w1 view = %+v", info)
		}
	}

	if err := rig.backend.Archive(ctx, "w2", true); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if got := rig.backend.List(false); len(got) != 1 || got[0].ID != "w1" {
		t.Fatalf("active after archive = %+v", got)
	}
	archived := rig.backend.List(true)
	if len(archived) != 1 || archived[0].ID != "w2" || !archived[0].Archived {
		t.Fatalf("archived = %+v", archived)
	}
	if _, ok, _ := rig.backend.Get("w2"); ok {
		t.Fatal("an archived worker must not appear in the active Get")
	}
}
