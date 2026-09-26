package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/itfs/worker"
	"github.com/niq-run/niq/core/impl/workers/niw"
)

func TestParseMountsParamTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	p := map[string]any{
		"mounts": []any{
			"~/.niq/projects/alpha/workspace",
			"~/uploads",
			"/abs/path",
			"~",
		},
	}
	mounts, err := parseMountsParam(p)
	if err != nil {
		t.Fatalf("parseMountsParam: %v", err)
	}
	want := []string{
		filepath.Join(home, ".niq", "projects", "alpha", "workspace"),
		filepath.Join(home, "uploads"),
		"/abs/path",
		home,
	}
	if len(mounts) != len(want) {
		t.Fatalf("got %d mounts, want %d: %v", len(mounts), len(want), mounts)
	}
	for i, m := range mounts {
		if m != want[i] {
			t.Errorf("mount[%d] = %q, want %q", i, m, want[i])
		}
	}
}

func TestParseMountsParamPathSugar(t *testing.T) {
	p := map[string]any{"path": "~/.niq/projects/beta/programs"}
	mounts, err := parseMountsParam(p)
	if err != nil {
		t.Fatalf("parseMountsParam: %v", err)
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".niq", "projects", "beta", "programs")
	if len(mounts) != 1 || mounts[0] != want {
		t.Fatalf("mounts = %v, want [%s]", mounts, want)
	}
}

// TestBuildNIWSpecRequiresRemoteParams verifies a NIW without the required
// params is rejected before any registry/listener is touched — so a zero
// BuildContext is safe here.
func TestBuildNIWSpecRequiresRemoteParams(t *testing.T) {
	var empty BuildContext

	if _, err := buildNIWSpec(empty, worker.WorkerConfig{ID: "peer"}); err == nil {
		t.Fatal("expected error for missing remote params, got nil")
	}

	// remote_worker_id alone (no URL) is still invalid.
	if _, err := buildNIWSpec(empty, worker.WorkerConfig{
		ID:     "peer",
		Params: map[string]any{"remote_worker_id": "remote-niw"},
	}); err == nil {
		t.Fatal("expected error for missing remote_url")
	}

	// A recipient is NOT required at build time: it is only the seed — the live
	// binding is a runtime property set via niw.recipient.* (like lark).
	if _, err := buildNIWSpec(empty, worker.WorkerConfig{
		ID: "peer",
		Params: map[string]any{
			"remote_url":        "http://127.0.0.1:19763",
			"remote_worker_id":  "from-b",
			"remote_credential": "tok",
		},
	}); err != nil {
		t.Fatalf("recipient should be optional (runtime/settable), got %v", err)
	}
}

// TestBuildNIWSpecReadsParams verifies the NIW spec picks up remote_url,
// remote_worker_id and recipient from Params (validated before connectivity, so
// a zero BuildContext is fine — specConnect is not reached past this check).
func TestBuildNIWSpecReadsParams(t *testing.T) {
	var empty BuildContext
	spec, err := buildNIWSpec(empty, worker.WorkerConfig{
		ID: "peer",
		Params: map[string]any{
			"remote_url":       "http://127.0.0.1:19763",
			"remote_worker_id": "from-b",
			"recipient":        "reason.local",
		},
	})
	if err != nil {
		t.Fatalf("buildNIWSpec: %v", err)
	}
	if spec.Type() != "niw" || spec.ID() != "peer" {
		t.Fatalf("unexpected spec: type=%s id=%s", spec.Type(), spec.ID())
	}
	// The Build closure carries the config; invoke it with a nil channel to
	// confirm it constructs a *niw.Worker.
	if w := spec.Build(nil); w == nil {
		t.Fatal("Build returned nil worker")
	} else if _, ok := w.(*niw.Worker); !ok {
		t.Fatalf("build returned %T, want *niw.Worker", w)
	}
}

// TestResolveSecret verifies env:/file:/plaintext credential resolution.
func TestResolveSecret(t *testing.T) {
	t.Setenv("NIW_TEST_CRED", "from-env")
	if got := resolveSecret("env:NIW_TEST_CRED"); got != "from-env" {
		t.Fatalf("env resolution = %q, want %q", got, "from-env")
	}

	f := filepath.Join(t.TempDir(), "cred")
	if err := os.WriteFile(f, []byte("from-file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolveSecret("file:" + f); got != "from-file" {
		t.Fatalf("file resolution = %q, want %q", got, "from-file")
	}

	if got := resolveSecret("plaintext"); got != "plaintext" {
		t.Fatalf("plaintext pass-through = %q, want %q", got, "plaintext")
	}
}
