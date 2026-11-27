package process

import (
	"sync"
	"testing"

	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// TestBuildRemoteIdentityDefaults verifies the default ACLs: a dialect-in
// worker gets the broad wildcard, while a niq link's far-side identity gets only what the
// bridge needs on the far side.
func TestBuildRemoteIdentityDefaults(t *testing.T) {
	normal := BuildRemoteIdentity(&IdentitySpec{ID: "w", Type: "mcp"})
	if len(normal.SubscribeAllow) != 1 || string(normal.SubscribeAllow[0].Type) != "*" {
		t.Fatalf("normal subscribe = %+v, want [*]", normal.SubscribeAllow)
	}
	if len(normal.PublishAllow) != 1 || string(normal.PublishAllow[0].Type) != "*" {
		t.Fatalf("normal publish = %+v, want [*]", normal.PublishAllow)
	}
	if !normal.Remote {
		t.Fatal("a third-party identity must be marked Remote")
	}

	peer := BuildRemoteIdentity(&IdentitySpec{ID: "far", Type: "niq"})
	if len(peer.SubscribeAllow) != 2 {
		t.Fatalf("niq subscribe = %+v, want worker.input + worker.discover", peer.SubscribeAllow)
	}
	if len(peer.PublishAllow) != 3 {
		t.Fatalf("niq publish = %+v, want worker.input + request.* + worker.ready", peer.PublishAllow)
	}
}

// memStore is a test-only bus.Store; the standalone file store is tested where
// it lives.
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
	return out, nil
}

func testRig(t *testing.T) (*busctl.Manager, *eventbus.LayeredRegistry, *eventbus.TokenSigner) {
	t.Helper()
	registry := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return busctl.New(newMemStore(), registry), registry, eventbus.NewTokenSigner([]byte("test-secret"))
}

// TestProvisionManagedProcessIsTemp verifies a launched child gets a credential
// and a temporary identity — never a persisted credential, because the token is
// handed to the child and dies with the host. Its grants ARE durable, so an ACL
// edit survives a restart.
func TestProvisionManagedProcessIsTemp(t *testing.T) {
	control, registry, signer := testRig(t)

	cred, err := ProvisionManagedProcess(registry, signer, control, IdentitySpec{ID: "child", Type: "mcp"})
	if err != nil {
		t.Fatalf("ProvisionManagedProcess: %v", err)
	}
	if cred == "" {
		t.Fatal("no credential minted")
	}
	id, ok := registry.Lookup("child")
	if !ok || id.Credential != cred || !id.Remote {
		t.Fatalf("runtime identity = %+v (ok=%v)", id, ok)
	}
	durable, ok := control.Lookup("child")
	if !ok || durable.Credential != "" || !durable.Remote {
		t.Fatalf("durable identity = %+v (ok=%v): credential must not be persisted", durable, ok)
	}
	if len(durable.PublishAllow) == 0 || len(durable.SubscribeAllow) == 0 {
		t.Fatalf("durable grants = %+v, want the type defaults", durable)
	}
}

// TestProvisionUnmanagedProcessPersists verifies a dial-in worker gets a
// durable credential, written through the bus control plane on first provision
// and reused afterwards.
func TestProvisionUnmanagedProcessPersists(t *testing.T) {
	control, _, signer := testRig(t)

	cred, err := ProvisionUnmanagedProcess(control, signer, IdentitySpec{ID: "remote", Type: "mcp"})
	if err != nil {
		t.Fatalf("ProvisionUnmanagedProcess: %v", err)
	}
	if cred == "" {
		t.Fatal("no credential minted")
	}
	id, ok := control.Lookup("remote")
	if !ok || id.Credential != cred || !id.Remote {
		t.Fatalf("durable identity = %+v (ok=%v)", id, ok)
	}

	// A later provision reuses the stored credential.
	again, err := ProvisionUnmanagedProcess(control, signer, IdentitySpec{ID: "remote", Type: "mcp"})
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if again != cred {
		t.Fatalf("credential regenerated: %q -> %q", cred, again)
	}
}

// TestUnmanagedReprojectAfterArchive verifies restoring an archived dial-in
// worker makes its identity routable again without re-minting anything.
func TestUnmanagedReprojectAfterArchive(t *testing.T) {
	control, registry, signer := testRig(t)
	cred, err := ProvisionUnmanagedProcess(control, signer, IdentitySpec{ID: "remote", Type: "mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Revoke("remote"); err != nil {
		t.Fatal(err)
	}
	again, err := ProvisionUnmanagedProcess(control, signer, IdentitySpec{ID: "remote", Type: "mcp"})
	if err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if again != cred {
		t.Fatalf("credential changed: %q -> %q", cred, again)
	}
	id, ok := registry.Lookup("remote")
	if !ok || id.Credential != cred {
		t.Fatalf("runtime identity = %+v (ok=%v)", id, ok)
	}
}

// TestManagedProcessReusesDurableGrants verifies an explicit grant (here: deny
// all) survives a relaunch instead of being re-seeded with the default.
func TestManagedProcessReusesDurableGrants(t *testing.T) {
	control, registry, signer := testRig(t)
	if _, err := ProvisionManagedProcess(registry, signer, control, IdentitySpec{ID: "child", Type: "mcp"}); err != nil {
		t.Fatal(err)
	}
	if err := control.UpdateACL("child", []event.PublishPattern{}, []event.EventPattern{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProvisionManagedProcess(registry, signer, control, IdentitySpec{ID: "child", Type: "mcp"}); err != nil {
		t.Fatal(err)
	}
	id, _ := control.Lookup("child")
	if len(id.PublishAllow) != 0 || len(id.SubscribeAllow) != 0 {
		t.Fatalf("grants were re-seeded: %+v", id)
	}
}
