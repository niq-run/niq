package eventbus

import (
	"path/filepath"
	"testing"

	"github.com/54c1/niq/core/bus"
)

func TestFileIdentityRegistryListSorted(t *testing.T) {
	r := &FileIdentityRegistry{
		identities: map[string]bus.Identity{
			"zeta":    {WorkerID: "zeta"},
			"alpha":   {WorkerID: "alpha"},
			"mike":    {WorkerID: "mike"},
			"bravo":   {WorkerID: "bravo"},
			"whiskey": {WorkerID: "whiskey"},
		},
	}

	// The backing store is a map, so iteration order is random; List must
	// return workers in deterministic, ID-sorted order across calls.
	first := r.List()
	second := r.List()
	if len(first) != len(r.identities) {
		t.Fatalf("List returned %d identities, want %d", len(first), len(r.identities))
	}
	for i := 1; i < len(first); i++ {
		if first[i].WorkerID <= first[i-1].WorkerID {
			t.Fatalf("List not sorted: %q after %q", first[i].WorkerID, first[i-1].WorkerID)
		}
	}
	for i := range first {
		if first[i].WorkerID != second[i].WorkerID {
			t.Fatalf("List order unstable across calls: %q vs %q", first[i].WorkerID, second[i].WorkerID)
		}
	}
}

// newTestRegistry returns a file-backed registry in a temp dir.
func newTestRegistry(t *testing.T) *FileIdentityRegistry {
	t.Helper()
	r, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestFileIdentityRegistryLifecycle(t *testing.T) {
	r := newTestRegistry(t)

	if err := r.Register(bus.Identity{WorkerID: "b", Credential: "x"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Register(bus.Identity{WorkerID: "a", Credential: "y"}); err != nil {
		t.Fatalf("register: %v", err)
	}

	ids := r.List()
	if len(ids) != 2 || ids[0].WorkerID != "a" || ids[1].WorkerID != "b" {
		t.Fatalf("List = %+v, want [a b] sorted", ids)
	}

	if err := r.Revoke("a"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, ok := r.Lookup("a"); ok {
		t.Fatal("lookup after revoke should miss")
	}
	if err := r.Revoke("missing"); err == nil {
		t.Fatal("revoking an unknown id should error")
	}
}
