package idfile

import (
	"path/filepath"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// TestRoundTripsGrantsAndDistinguishesNilFromEmpty verifies the on-disk format:
// "never seeded" (nil) and "deny all" (explicit empty) are different states, so
// a later connect knows whether to seed the type default.
func TestRoundTripsGrantsAndDistinguishesNilFromEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	store, err := New(path)
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

	reopened, err := New(path)
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

// TestMergeCreatesAndPreserves verifies the migration writer keeps existing
// records and creates the file even when there is nothing to merge (its presence
// marks the migration done).
func TestMergeCreatesAndPreserves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identities.json")
	if err := Merge(path, nil); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	if ids, _ := store.All(); len(ids) != 0 {
		t.Fatalf("empty migration wrote %+v", ids)
	}
	if err := store.Save("keep", corebus.Identity{WorkerID: "keep", Type: "timer"}); err != nil {
		t.Fatal(err)
	}
	if err := Merge(path, []corebus.Identity{{WorkerID: "moved", Type: "mcp", Remote: true, Credential: "tok"}}); err != nil {
		t.Fatalf("merge: %v", err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	ids, _ := reopened.All()
	if len(ids) != 2 {
		t.Fatalf("merged ids = %+v, want keep + moved", ids)
	}
}
