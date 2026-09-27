package eventbus

import (
	"path/filepath"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// newLayered builds a LayeredRegistry over a fresh file registry.
func newLayered(t *testing.T) *LayeredRegistry {
	t.Helper()
	file, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "id.json"))
	if err != nil {
		t.Fatal(err)
	}
	return NewLayeredRegistry(file)
}

func ident(id string) corebus.Identity {
	return corebus.Identity{
		WorkerID:       id,
		Type:           "mcp",
		Credential:     "tok",
		Remote:         true,
		PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
		SubscribeAllow: []event.EventPattern{{Type: "*"}},
	}
}

// TestTempIdentityLivesInMemory verifies a temp id is visible to Lookup/List
// but is NOT persisted to the durable tier.
func TestTempIdentityLivesInMemory(t *testing.T) {
	r := newLayered(t)
	if err := r.RegisterTemp(ident("temp-1")); err != nil {
		t.Fatalf("RegisterTemp: %v", err)
	}
	id, ok := r.Lookup("temp-1")
	if !ok || !id.Remote {
		t.Fatalf("temp identity not visible to Lookup: %v %v", id, ok)
	}
	found := false
	for _, e := range r.List() {
		if e.WorkerID == "temp-1" {
			found = true
		}
	}
	if !found {
		t.Fatal("temp identity not in List")
	}
	// It must NOT appear in the durable file tier.
	if _, ok := r.durable.Lookup("temp-1"); ok {
		t.Fatal("temp identity leaked into the durable tier")
	}
}

// TestDurableAndTempCoexistWithoutShadowing verifies id ownership is explicit:
// a temp id cannot shadow a durable one and vice versa.
func TestDurableAndTempCoexistWithoutShadowing(t *testing.T) {
	r := newLayered(t)
	if err := r.Register(ident("durable-1")); err != nil {
		t.Fatal(err)
	}
	// Temp cannot claim an already-durable id.
	if err := r.RegisterTemp(ident("durable-1")); err == nil {
		t.Fatal("temp registration must refuse to shadow a durable id")
	}
	// Durable (via the layered Register, the production path) cannot claim an
	// id already owned by the temp tier.
	if err := r.RegisterTemp(ident("temp-1")); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(ident("temp-1")); err == nil {
		t.Fatal("durable registration must refuse to shadow a temp id")
	}
}

// TestRevokeTemp verifies RevokeTemp removes only the in-memory identity.
func TestRevokeTemp(t *testing.T) {
	r := newLayered(t)
	if err := r.RegisterTemp(ident("temp-2")); err != nil {
		t.Fatal(err)
	}
	r.RevokeTemp("temp-2")
	if _, ok := r.Lookup("temp-2"); ok {
		t.Fatal("temp identity not removed by RevokeTemp")
	}
}
