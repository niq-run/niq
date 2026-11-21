package busctl

import (
	"sort"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// memStore is a test-only bus.Store. Durable storage is exercised where it
// lives (the standalone file store), so the control plane only needs a map.
type memStore struct {
	mu  sync.RWMutex
	ids map[string]corebus.Identity
}

func newMemStore() *memStore { return &memStore{ids: map[string]corebus.Identity{}} }

func (s *memStore) Load(id string) (corebus.Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	idt, ok := s.ids[id]
	return idt, ok, nil
}

func (s *memStore) Save(id string, idt corebus.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[id] = idt
	return nil
}

func (s *memStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
	return nil
}

func (s *memStore) All() ([]corebus.Identity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]corebus.Identity, 0, len(s.ids))
	for _, idt := range s.ids {
		out = append(out, idt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].WorkerID < out[j].WorkerID })
	return out, nil
}

func testPlane(t *testing.T) (*ControlPlane, *eventbus.LayeredRegistry) {
	t.Helper()
	runtime := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return New(newMemStore(), runtime), runtime
}

// TestRegisterProjectsAndRejectsDuplicates verifies the durable write and the
// runtime projection happen together, and that identity ownership is explicit.
func TestRegisterProjectsAndRejectsDuplicates(t *testing.T) {
	cp, runtime := testPlane(t)
	id := corebus.Identity{
		WorkerID:       "w1",
		Type:           "reason",
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
		SubscribeAllow: []event.EventPattern{event.NewPattern("worker.discover")},
	}
	if err := cp.Register(id); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got, ok := runtime.Lookup("w1"); !ok || got.Type != "reason" {
		t.Fatalf("runtime = %+v (ok=%v)", got, ok)
	}
	if err := cp.Register(id); err == nil {
		t.Fatal("duplicate Register succeeded")
	}
}

// TestUpdateACLPersistsAndProjects verifies a grant edit is one call that
// reaches both the store and the live registry.
func TestUpdateACLPersistsAndProjects(t *testing.T) {
	cp, runtime := testPlane(t)
	if err := cp.Register(corebus.Identity{WorkerID: "w1", Type: "reason"}); err != nil {
		t.Fatal(err)
	}
	pub := []event.PublishPattern{event.NewPublishPattern("worker.ready")}
	sub := []event.EventPattern{event.NewPattern("worker.discover")}
	if err := cp.UpdateACL("w1", pub, sub); err != nil {
		t.Fatalf("UpdateACL: %v", err)
	}
	idt, _ := cp.Lookup("w1")
	if len(idt.PublishAllow) != 1 || len(idt.SubscribeAllow) != 1 {
		t.Fatalf("durable = %+v", idt)
	}
	live, _ := runtime.Lookup("w1")
	if len(live.PublishAllow) != 1 || len(live.SubscribeAllow) != 1 {
		t.Fatalf("runtime = %+v", live)
	}
	if err := cp.UpdateACL("nope", nil, nil); err == nil {
		t.Fatal("UpdateACL on an unknown identity succeeded")
	}
}

// TestRevokeClearsRuntimeEvenWithoutDurableRecord verifies a destroy path can
// call Revoke unconditionally: a temp or legacy identity has no store record but
// must stop being routable.
func TestRevokeClearsRuntimeEvenWithoutDurableRecord(t *testing.T) {
	cp, runtime := testPlane(t)
	if err := runtime.Register(corebus.Identity{WorkerID: "temp", Type: "mcp", Remote: true}); err != nil {
		t.Fatal(err)
	}
	if err := cp.Revoke("temp"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := runtime.Lookup("temp"); ok {
		t.Fatal("runtime identity survived Revoke")
	}
	if err := cp.Revoke("temp"); err != nil {
		t.Fatalf("second Revoke must be a no-op: %v", err)
	}
}

// TestProjectRestoresDroppedProjection verifies a caller can re-apply a durable
// identity after dropping the runtime entry (archive → unarchive).
func TestProjectRestoresDroppedProjection(t *testing.T) {
	cp, runtime := testPlane(t)
	if err := cp.Register(corebus.Identity{WorkerID: "w1", Type: "reason"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Revoke("w1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Lookup("w1"); ok {
		t.Fatal("precondition: runtime entry should be gone")
	}
	if err := cp.Project("w1"); err != nil {
		t.Fatalf("Project: %v", err)
	}
	if _, ok := runtime.Lookup("w1"); !ok {
		t.Fatal("Project did not restore the runtime identity")
	}
	if err := cp.Project("nope"); err == nil {
		t.Fatal("Project on an unknown identity succeeded")
	}
}

// TestAwaitingLaunchIsNotProjected verifies a host-launched child's durable
// record reserves its grants but is not routable until a credential is minted
// for that launch.
func TestAwaitingLaunchIsNotProjected(t *testing.T) {
	cp, runtime := testPlane(t)
	child := corebus.Identity{WorkerID: "child", Type: "mcp", Remote: true}
	if err := cp.Register(child); err != nil {
		t.Fatal(err)
	}
	if _, ok := runtime.Lookup("child"); ok {
		t.Fatal("a credential-less remote identity must not be projected")
	}
	if err := cp.SetCredential("child", "tok"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	live, ok := runtime.Lookup("child")
	if !ok || live.Credential != "tok" || !live.Remote {
		t.Fatalf("runtime = %+v (ok=%v)", live, ok)
	}
	if err := cp.SetCredential("nope", "tok"); err == nil {
		t.Fatal("SetCredential on an unknown identity succeeded")
	}
}

// TestSeedProjectsDurableIdentities verifies startup seeds the runtime registry
// before the bus serves, and applies the same awaiting-launch rule.
func TestSeedProjectsDurableIdentities(t *testing.T) {
	store := newMemStore()
	durable := []corebus.Identity{
		{WorkerID: "inproc", Type: "timer", PublishAllow: []event.PublishPattern{event.NewPublishPattern("request.*")}},
		{WorkerID: "remote", Type: "mcp", Remote: true, Credential: "tok"},
		{WorkerID: "child", Type: "mcp", Remote: true},
	}
	for _, id := range durable {
		if err := store.Save(id.WorkerID, id); err != nil {
			t.Fatal(err)
		}
	}

	runtime := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	cp := New(store, runtime)
	if err := cp.Seed(nil); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if _, ok := runtime.Lookup("inproc"); !ok {
		t.Fatal("in-process identity not seeded")
	}
	if id, ok := runtime.Lookup("remote"); !ok || id.Credential != "tok" {
		t.Fatalf("remote identity = %+v (ok=%v)", id, ok)
	}
	if _, ok := runtime.Lookup("child"); ok {
		t.Fatal("awaiting-launch identity must not be seeded")
	}
}

// TestSeedSkipsExcludedIdentities verifies an archived worker's durable grants
// stay on disk but its identity is not routable at startup.
func TestSeedSkipsExcludedIdentities(t *testing.T) {
	store := newMemStore()
	if err := store.Save("archived", corebus.Identity{WorkerID: "archived", Type: "mcp", Remote: true, Credential: "tok"}); err != nil {
		t.Fatal(err)
	}
	runtime := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	if err := New(store, runtime).Seed([]string{"archived"}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if _, ok := runtime.Lookup("archived"); ok {
		t.Fatal("an archived identity was seeded")
	}
	if _, ok, _ := store.Load("archived"); !ok {
		t.Fatal("seed/exclude must not delete the durable record")
	}
}
