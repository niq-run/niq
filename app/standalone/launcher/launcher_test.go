package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/store"
)

// TestWaitStartReportsExit asserts a start whose child dies before serving is
// reported as a failure naming the exit and pointing at the log.
func TestWaitStartReportsExit(t *testing.T) {
	t.Setenv("NIQ_HOME", t.TempDir())
	exited := make(chan error, 1)
	exited <- errors.New("exit status 1")
	_, err := waitStart("ghost", exited)
	if err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("err = %v, want an exit report", err)
	}
	if !strings.Contains(err.Error(), store.LogPath("ghost")) {
		t.Fatalf("err should point at the project log: %v", err)
	}
}

// TestLogTail returns only the trailing lines, which is what the start failure
// appends after the log path.
func TestLogTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\nfive\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := logTail(path, 2); got != "four\nfive" {
		t.Fatalf("logTail = %q", got)
	}
	if got := logTail(filepath.Join(t.TempDir(), "missing"), 2); got != "" {
		t.Fatalf("missing file should yield no tail, got %q", got)
	}
}
