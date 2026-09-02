package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/pkg/eventbus"
)

func boolPtr(b bool) *bool { return &b }

func testSupervisor() *UnmanagedSupervisor {
	sv := NewUnmanagedSupervisor("http://127.0.0.1:1", "", func(string, ...any) {})
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

// TestUnmanagedSupervisorSingleInstance verifies that starting the same worker
// id twice while one is running kills the old child first (no two live
// processes shadowing the bus session). The second Start replaces the first, so
// the marker advances by exactly one fresh launch and only one child is alive.
func TestUnmanagedSupervisorSingleInstance(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs.log")
	sv := testSupervisor()
	spec := ProjectWorker{
		ID: "lark", Type: "x",
		Command: []string{"/bin/sh", "-c", "echo run >> " + marker + "; sleep 60"},
	}
	if err := sv.Start(spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitMarker(t, marker, 1, 3*time.Second)

	// Second Start for the SAME id while the first child is alive: it must kill
	// the old child and relaunch, leaving exactly one live process.
	n := countMarker(marker) // ==1
	if err := sv.Start(spec); err != nil {
		t.Fatalf("Start #2: %v", err)
	}
	// The replacement child must appear (so marker grows by one).
	waitMarker(t, marker, n+1, 3*time.Second)

	sv.Shutdown()
	// After shutdown, the total distinct starts should be small (killed ones
	// don't spawn extra children; backoff/persist would inflate it past here).
	if got := countMarker(marker); got > n+1 {
		t.Fatalf("double Start produced extra live children: marker = %d", got)
	}
}

// TestUnmanagedSupervisorStdoutLog verifies a worker's stdout is written to
// <workersRoot>/<id>/stdout.log instead of being lost on the supervisor's own
// output.
func TestUnmanagedSupervisorStdoutLog(t *testing.T) {
	root := t.TempDir()
	sv := NewUnmanagedSupervisor("http://127.0.0.1:1", root, func(string, ...any) {})
	sv.initialDelay = 10 * time.Millisecond
	sv.maxDelay = 30 * time.Millisecond
	sv.stableAfter = time.Hour
	spec := ProjectWorker{
		ID: "lark", Type: "x",
		Command: []string{"/bin/sh", "-c", "echo hello-from-worker; sleep 1"},
	}
	if err := sv.Start(spec); err != nil {
		t.Fatalf("Start: %v", err)
	}

	logFile := filepath.Join(root, "lark", "stdout.log")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(logFile); err == nil && strings.Contains(string(b), "hello-from-worker") {
			sv.Shutdown()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let the child finish on its own; verify the file exists with content.
	sv.Shutdown()
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read stdout.log: %v", err)
	}
	if !strings.Contains(string(b), "hello-from-worker") {
		t.Fatalf("stdout.log = %q, want it to contain worker output", string(b))
	}
}

// TestUnmanagedSupervisorStateDir verifies each external worker gets a
// NIQ_STATE_DIR pointing at its persistent state directory.
func TestUnmanagedSupervisorStateDir(t *testing.T) {
	root := t.TempDir()
	sv := NewUnmanagedSupervisor("http://127.0.0.1:1", root, func(string, ...any) {})
	sv.initialDelay = 10 * time.Millisecond
	sv.maxDelay = 30 * time.Millisecond
	sv.stableAfter = time.Hour
	spec := ProjectWorker{
		ID: "lark", Type: "x",
		Command: []string{"/bin/sh", "-c", "echo \"$NIQ_STATE_DIR\" > $NIQ_STATE_DIR/where.log; sleep 60"},
	}
	if err := sv.Start(spec); err != nil {
		t.Fatalf("Start: %v", err)
	}

	wantDir := filepath.Join(root, "lark")
	gotFile := filepath.Join(wantDir, "where.log")

	// Wait for the worker to have run and written into its state dir.
	deadline := time.Now().Add(3 * time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(gotFile); err == nil {
			raw = b
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	sv.Shutdown()

	if len(raw) == 0 {
		t.Fatalf("NIQ_STATE_DIR was not set or dir not writable (no %s)", gotFile)
	}
	if strings.TrimSpace(string(raw)) != wantDir {
		t.Fatalf("NIQ_STATE_DIR = %q, want %q", strings.TrimSpace(string(raw)), wantDir)
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
	p, err := CreateProject("proj", &TemplateConfig{Workers: []WorkerConfig{
		{Type: "mcp", ID: "ext", Managed: boolPtr(false), Command: []string{"npx", "x"}, Subscriptions: []SubscriptionSpec{{Type: "*"}}},
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
