package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testSupervisor builds a supervisor whose children log to a temp dir rather
// than inheriting the test process's stdout: a leaked child holding the test
// binary's output pipe would keep `go test` waiting for it long after PASS.
func testSupervisor(t *testing.T) *ProcessSupervisor {
	t.Helper()
	sv := NewProcessSupervisor("http://127.0.0.1:1", t.TempDir(), func(string, ...any) {})
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

// TestProcessSupervisorRestartAndStop verifies a crashing external worker is
// restarted with backoff, and Stop prevents further restarts.
func TestProcessSupervisorRestartAndStop(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs.log")
	sv := testSupervisor(t)
	spec := Spec{
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

// TestProcessSupervisorManualRestart verifies Restart kills the running
// process and launches a fresh one.
func TestProcessSupervisorManualRestart(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs.log")
	sv := testSupervisor(t)
	spec := Spec{
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

// TestProcessSupervisorShutdown verifies Shutdown stops every worker and the
// supervision loops exit (no hang).
func TestProcessSupervisorShutdown(t *testing.T) {
	sv := testSupervisor(t)
	for _, id := range []string{"a", "b"} {
		if err := sv.Start(Spec{ID: id, Type: "x", Command: []string{"/bin/sh", "-c", "sleep 60"}}); err != nil {
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

// TestProcessSupervisorSingleInstance verifies that starting the same worker
// id twice while one is running kills the old child first (no two live
// processes shadowing the bus session). The second Start replaces the first, so
// the marker advances by exactly one fresh launch and only one child is alive.
func TestProcessSupervisorSingleInstance(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "runs.log")
	sv := testSupervisor(t)
	spec := Spec{
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

// TestProcessSupervisorStdoutLog verifies a worker's stdout is written to
// <workersRoot>/<id>/stdout.log instead of being lost on the supervisor's own
// output.
func TestProcessSupervisorStdoutLog(t *testing.T) {
	root := t.TempDir()
	sv := NewProcessSupervisor("http://127.0.0.1:1", root, func(string, ...any) {})
	sv.initialDelay = 10 * time.Millisecond
	sv.maxDelay = 30 * time.Millisecond
	sv.stableAfter = time.Hour
	spec := Spec{
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

// TestProcessSupervisorStateDir verifies each external worker gets a
// NIQ_STATE_DIR pointing at its persistent state directory.
func TestProcessSupervisorStateDir(t *testing.T) {
	root := t.TempDir()
	sv := NewProcessSupervisor("http://127.0.0.1:1", root, func(string, ...any) {})
	sv.initialDelay = 10 * time.Millisecond
	sv.maxDelay = 30 * time.Millisecond
	sv.stableAfter = time.Hour
	spec := Spec{
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

func findStatus(sts []ProcessStatus, id string) *ProcessStatus {
	for i := range sts {
		if sts[i].ID == id {
			return &sts[i]
		}
	}
	return nil
}
