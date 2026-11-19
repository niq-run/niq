package busctl

import (
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

func testPlane(t *testing.T) (*ControlPlane, *eventbus.LayeredRegistry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	runtime := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return New(store, runtime), runtime, path
}

// TestRegisterProjectsAndRejectsDuplicates verifies the durable write and the
// runtime projection happen together, and that identity ownership is explicit.
func TestRegisterProjectsAndRejectsDuplicates(t *testing.T) {
	cp, runtime, _ := testPlane(t)
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
	cp, runtime, _ := testPlane(t)
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
	cp, runtime, _ := testPlane(t)
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
	cp, runtime, _ := testPlane(t)
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
	cp, runtime, _ := testPlane(t)
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
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
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
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
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

// TestFileStoreRoundTripsGrantsAndDistinguishesNilFromEmpty verifies the on-disk
// format: "never seeded" (nil) and "deny all" (explicit empty) are different
// states, so a later connect knows whether to seed the type default.
func TestFileStoreRoundTripsGrantsAndDistinguishesNilFromEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("unseeded", corebus.Identity{WorkerID: "unseeded", Type: "reason"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save("denied", corebus.Identity{
		WorkerID:       "denied",
		Type:           "reason",
		PublishAllow:   []event.PublishPattern{},
		SubscribeAllow: []event.EventPattern{},
	}); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	unseeded, ok, err := reopened.Load("unseeded")
	if err != nil || !ok {
		t.Fatalf("load unseeded: %v %v", ok, err)
	}
	if unseeded.PublishAllow != nil || unseeded.SubscribeAllow != nil {
		t.Fatalf("unseeded = %+v, want nil grants", unseeded)
	}
	denied, ok, err := reopened.Load("denied")
	if err != nil || !ok {
		t.Fatalf("load denied: %v %v", ok, err)
	}
	if denied.PublishAllow == nil || denied.SubscribeAllow == nil || len(denied.PublishAllow) != 0 {
		t.Fatalf("denied = %+v, want explicit empty grants", denied)
	}
}

// TestWriteFileMergesForMigration verifies the migration writer keeps existing
// records and creates the file even when there is nothing to merge (its presence
// marks the migration done).
func TestWriteFileMergesForMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	if err := WriteFile(path, nil); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if ids, _ := store.All(); len(ids) != 0 {
		t.Fatalf("empty migration wrote %+v", ids)
	}
	if err := store.Save("keep", corebus.Identity{WorkerID: "keep", Type: "timer"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []corebus.Identity{{WorkerID: "moved", Type: "mcp", Remote: true, Credential: "tok"}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	reopened, err := NewFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := reopened.All()
	if len(ids) != 2 {
		t.Fatalf("merged ids = %+v, want keep + moved", ids)
	}
}
