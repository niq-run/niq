package swarm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/54c1/niq/pkg/service/eventbus"
)

func boolPtr(b bool) *bool { return &b }

func testSupervisor() *UnmanagedSupervisor {
	sv := NewUnmanagedSupervisor("http://127.0.0.1:1", func(string, ...any) {})
	sv.initialDelay = 10 * time.Millisecond
	sv.maxDelay = 30 * time.Millisecond
	sv.stableAfter = time.Hour
	return sv
}

func countMarker(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "run")
}

func waitMarker(t *testing.T, path string, min int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if countMarker(path) >= min {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("marker %s did not reach %d runs", path, min)
}

// TestUnmanagedSupervisorRestartAndStop verifies a crashing external worker is
// restarted with backoff, and Stop prevents further restarts.
func TestUnmanagedSupervisorRestartAndStop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs.log")
	sv := testSupervisor()
	spec := ProjectWorker{
		ID: "crashy", Type: "x",
		Command: []string{"/bin/sh", "-c", "echo run >> " + marker + "; exit 1"},
	}
	if err := sv.Start(spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitMarker(t, marker, 3, 3*time.Second)

	if err := sv.Stop("crashy"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	n := countMarker(marker)
	time.Sleep(200 * time.Millisecond)
	if got := countMarker(marker); got > n+1 {
		t.Fatalf("marker kept growing after Stop: %d → %d", n, got)
	}
	st := findStatus(sv.List(), "crashy")
	if st == nil || st.State != "stopped" {
		t.Fatalf("status after stop = %+v", st)
	}
}

// TestUnmanagedSupervisorManualRestart verifies Restart kills the running
// process and launches a fresh one.
func TestUnmanagedSupervisorManualRestart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs.log")
	sv := testSupervisor()
	spec := ProjectWorker{
		ID: "svc", Type: "x",
		Command: []string{"/bin/sh", "-c", "echo run >> " + marker + "; sleep 60"},
	}
	if err := sv.Start(spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitMarker(t, marker, 1, 3*time.Second)

	if err := sv.Restart("svc"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	waitMarker(t, marker, 2, 3*time.Second)
	sv.Shutdown()
}

// TestUnmanagedSupervisorShutdown verifies Shutdown stops every worker and the
// supervision loops exit (no hang).
func TestUnmanagedSupervisorShutdown(t *testing.T) {
	sv := testSupervisor()
	for _, id := range []string{"a", "b"} {
		if err := sv.Start(ProjectWorker{ID: id, Type: "x", Command: []string{"/bin/sh", "-c", "sleep 60"}}); err != nil {
			t.Fatalf("Start %s: %v", id, err)
		}
	}
	done := make(chan struct{})
	go func() {
		sv.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown hung")
	}
	for _, st := range sv.List() {
		if st.State != "stopped" {
			t.Fatalf("worker %s still %s after Shutdown", st.ID, st.State)
		}
	}
}

func findStatus(sts []UnmanagedStatus, id string) *UnmanagedStatus {
	for i := range sts {
		if sts[i].ID == id {
			return &sts[i]
		}
	}
	return nil
}

// TestProvisionUnmanaged verifies a credential is generated and persisted on
// first launch, reused afterwards, and the identity is registered with the
// declared allow lists.
func TestProvisionUnmanaged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p, err := CreateProject("proj", &SwarmConfig{Workers: []WorkerConfig{
		{Type: "mcp", ID: "ext", Managed: boolPtr(false), Command: []string{"npx", "x"}, Subscriptions: []string{"tool.requested"}},
	}})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(t.TempDir(), "id", "identities.json"))
	if err != nil {
		t.Fatal(err)
	}

	spec := p.Workers[0]
	if err := provisionUnmanaged(registry, "proj", &spec); err != nil {
		t.Fatalf("provision: %v", err)
	}
	if spec.Credential == "" {
		t.Fatal("credential not generated")
	}

	// Credential persisted to project.json.
	reloaded, err := LoadProject("proj")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Workers[0].Credential != spec.Credential {
		t.Fatalf("credential not persisted: got %q", reloaded.Workers[0].Credential)
	}

	// Identity registered with credential + declared subscriptions.
	id, ok := registry.Lookup("ext")
	if !ok {
		t.Fatal("identity not registered")
	}
	if id.Credential != spec.Credential {
		t.Fatalf("registry credential mismatch")
	}
	if len(id.SubscribeAllow) != 1 {
		t.Fatalf("subscribe allow = %v, want 1 pattern", id.SubscribeAllow)
	}

	// Re-provision reuses the persisted credential.
	spec2 := reloaded.Workers[0]
	if err := provisionUnmanaged(registry, "proj", &spec2); err != nil {
		t.Fatalf("re-provision: %v", err)
	}
	if spec2.Credential != spec.Credential {
		t.Fatalf("credential regenerated: %q → %q", spec.Credential, spec2.Credential)
	}
}
