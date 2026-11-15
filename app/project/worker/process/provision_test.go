package process

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/impl/eventbus"
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
// worker gets the broad wildcard, while a remote-niw peer gets only what the
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

	niw := BuildRemoteIdentity(&IdentitySpec{ID: "far", Type: "remote-niw"})
	if len(niw.SubscribeAllow) != 2 {
		t.Fatalf("remote-niw subscribe = %+v, want worker.input + worker.discover", niw.SubscribeAllow)
	}
	if len(niw.PublishAllow) != 3 {
		t.Fatalf("remote-niw publish = %+v, want worker.input + request.* + worker.ready", niw.PublishAllow)
	}
}

// TestProvisionManagedProcessIsTemp verifies a launched child gets a credential
// and an in-memory identity — never a durable one, because the token is handed
// to the child and dies with the host.
func TestProvisionManagedProcessIsTemp(t *testing.T) {
	dir := t.TempDir()
	durable, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := eventbus.NewLayeredRegistry(durable)
	signer := eventbus.NewTokenSigner([]byte("test-secret"))

	spec := &IdentitySpec{ID: "child", Type: "mcp"}
	if err := ProvisionManagedProcess(registry, signer, spec); err != nil {
		t.Fatalf("ProvisionManagedProcess: %v", err)
	}
	if spec.Credential == "" {
		t.Fatal("no credential minted")
	}
	id, ok := registry.Lookup("child")
	if !ok || id.Credential != spec.Credential || !id.Remote {
		t.Fatalf("identity = %+v (ok=%v)", id, ok)
	}
	if _, ok := durable.Lookup("child"); ok {
		t.Fatal("a managed-process credential must not be persisted durably")
	}
}

// TestProvisionUnmanagedProcessPersists verifies a dial-in worker gets a
// durable credential, written back to the declaration on first provision and
// reused afterwards.
func TestProvisionUnmanagedProcessPersists(t *testing.T) {
	dir := t.TempDir()
	durable, err := eventbus.NewFileIdentityRegistry(filepath.Join(dir, "identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	registry := eventbus.NewLayeredRegistry(durable)
	signer := eventbus.NewTokenSigner([]byte("test-secret"))
	store := fakeCredStore{}

	spec := &IdentitySpec{ID: "remote", Type: "mcp"}
	if err := ProvisionUnmanagedProcess(registry, signer, store, spec); err != nil {
		t.Fatalf("ProvisionUnmanagedProcess: %v", err)
	}
	if spec.Credential == "" || store["remote"] != spec.Credential {
		t.Fatalf("credential = %q, store = %v", spec.Credential, store)
	}
	id, ok := durable.Lookup("remote")
	if !ok || id.Credential != spec.Credential || !id.Remote {
		t.Fatalf("durable identity = %+v (ok=%v)", id, ok)
	}

	// A later provision carrying the persisted credential must reuse it.
	again := &IdentitySpec{ID: "remote", Type: "mcp", Credential: spec.Credential}
	if err := ProvisionUnmanagedProcess(registry, signer, store, again); err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if again.Credential != spec.Credential {
		t.Fatalf("credential regenerated: %q -> %q", spec.Credential, again.Credential)
	}
}

type fakeCredStore map[string]string

func (f fakeCredStore) SetCredential(id, credential string) error {
	f[id] = credential
	return nil
}
