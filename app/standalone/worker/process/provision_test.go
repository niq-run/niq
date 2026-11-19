package process

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/itfs/event"
)

// TestResolveSecret verifies env:/file:/plaintext credential resolution.
func TestResolveSecret(t *testing.T) {
	t.Setenv("NIW_TEST_CRED", "from-env")
	if got := ResolveSecret("env:NIW_TEST_CRED"); got != "from-env" {
		t.Fatalf("env resolution = %q, want %q", got, "from-env")
	}

	f := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(f, []byte("from-file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveSecret("file:" + f); got != "from-file" {
		t.Fatalf("file resolution = %q, want %q", got, "from-file")
	}

	if got := ResolveSecret("plaintext"); got != "plaintext" {
		t.Fatalf("plaintext pass-through = %q, want %q", got, "plaintext")
	}
}

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

func testRig(t *testing.T) (*busctl.ControlPlane, *eventbus.LayeredRegistry, *eventbus.TokenSigner) {
	t.Helper()
	store, err := busctl.NewFileStore(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())
	return busctl.New(store, registry), registry, eventbus.NewTokenSigner([]byte("test-secret"))
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
