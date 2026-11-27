package worker

import (
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/vault"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// newSealingRig wires a real (temporary) vault into a test rig and marks the
// fake type's "note" param as a credential, which is what turns sealing on.
func newSealingRig(t *testing.T) (*testRig, *vault.Vault) {
	t.Helper()
	rig := newTestRig(t)
	v := vault.New(t.TempDir())
	rig.backend.secrets = v
	rig.cp.types.MarkSecret("fake", "note")
	return rig, v
}

func TestCreateSealsLiteralCredential(t *testing.T) {
	rig, v := newSealingRig(t)
	ctx := context.Background()

	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{
			ID:     "w1",
			Type:   "fake",
			Params: map[string]any{"name": "alpha", "note": "sk-literal-token"},
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, ok, _ := rig.manifests.Get("w1")
	if !ok {
		t.Fatal("manifest missing")
	}
	ref, _ := m.Params["note"].(string)
	if ref != "vault:test/w1/note" {
		t.Fatalf("params[note] = %q, want a vault reference", ref)
	}
	if got, err := v.Get("test/w1/note"); err != nil || got != "sk-literal-token" {
		t.Fatalf("vault entry = %q, %v", got, err)
	}
	// The non-secret param is untouched.
	if m.Params["name"] != "alpha" {
		t.Fatalf("params[name] = %v", m.Params["name"])
	}
	// The caller's map is not mutated in place.
	if got := rig.backend.secrets.Resolve(ref); got != "sk-literal-token" {
		t.Fatalf("Resolve(ref) = %q", got)
	}
}

func TestSealingLeavesExistingReferencesAlone(t *testing.T) {
	rig, v := newSealingRig(t)
	ctx := context.Background()

	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{
			ID:     "w2",
			Type:   "fake",
			Params: map[string]any{"name": "beta", "note": "env:SHARED_TOKEN"},
		},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	m, _, _ := rig.manifests.Get("w2")
	if m.Params["note"] != "env:SHARED_TOKEN" {
		t.Fatalf("params[note] = %v, want the reference preserved", m.Params["note"])
	}
	if list, _ := v.List(); len(list) != 0 {
		t.Fatalf("vault = %+v, want nothing written for a reference", list)
	}
}

func TestUpdateSealsAndReSealsIdempotently(t *testing.T) {
	rig, v := newSealingRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w3", Type: "fake", Params: map[string]any{"name": "gamma"}},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := rig.backend.Update(ctx, "w3", coreworker.WorkerPatch{
		Params: map[string]any{"note": "first-token"},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got, _ := v.Get("test/w3/note"); got != "first-token" {
		t.Fatalf("vault = %q", got)
	}

	// Writing a reference back must not re-seal it into a second entry.
	if _, err := rig.backend.Update(ctx, "w3", coreworker.WorkerPatch{
		Params: map[string]any{"note": "vault:test/w3/note"},
	}); err != nil {
		t.Fatalf("Update ref: %v", err)
	}
	m, _, _ := rig.manifests.Get("w3")
	if m.Params["note"] != "vault:test/w3/note" {
		t.Fatalf("params[note] = %v", m.Params["note"])
	}
	if list, _ := v.List(); len(list) != 1 {
		t.Fatalf("vault = %+v, want one entry", list)
	}
}

func TestDestroyKeepsCredentials(t *testing.T) {
	rig, v := newSealingRig(t)
	ctx := context.Background()
	if _, err := rig.backend.Create(ctx, coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{ID: "w4", Type: "fake", Params: map[string]any{"name": "delta", "note": "keep-me"}},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := rig.backend.Destroy(ctx, "w4"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// A credential may be shared with other workers and other projects: destroying
	// a worker must not delete it.
	if got, err := v.Get("test/w4/note"); err != nil || got != "keep-me" {
		t.Fatalf("vault entry after destroy = %q, %v; want it kept", got, err)
	}
}

func TestSealingRejectsNonStringCredential(t *testing.T) {
	rig, _ := newSealingRig(t)
	_, err := rig.backend.Create(context.Background(), coreworker.WorkerSpec{
		WorkerConfig: coreworker.WorkerConfig{
			ID:     "w5",
			Type:   "fake",
			Params: map[string]any{"name": "eps", "note": map[string]any{"nested": "x"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("err = %v, want a type error", err)
	}
}
