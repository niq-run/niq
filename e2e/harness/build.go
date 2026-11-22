// Package harness is the shared foundation for the niq end-to-end suites.
//
// Every suite drives the product the way a user does — a built `niq` binary,
// its exit codes and output, the state it leaves on disk, and the HTTP it
// serves — so nothing here imports the packages under test. That is what keeps
// a suite meaningful: it fails when behaviour changes, not when a signature does.
//
// It is a plain package (no build tag) so `go build ./...` compiles it; only the
// suites themselves are excluded from a normal `go test ./...`.
package harness

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// binPath is the CLI built once per test binary by Main.
var binPath string

// Main builds the CLI, runs this process's tests, and removes the build.
// A suite's TestMain is the same two lines:
//
//	func TestMain(m *testing.M) { os.Exit(harness.Main(m)) }
func Main(m *testing.M) int {
	dir, err := os.MkdirTemp("", "niq-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)

	// Built by import path, not by a path relative to this file, so the suite
	// can move without breaking the build step.
	binPath = filepath.Join(dir, "niq")
	build := exec.Command("go", "build", "-o", binPath, "github.com/niq-run/niq/app/publish/cmd")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build the CLI: %v\n", err)
		return 1
	}
	return m.Run()
}

// Bin is the binary under test.
func Bin(t *testing.T) string {
	t.Helper()
	if binPath == "" {
		t.Fatal("harness.Main(m) must be called from TestMain")
	}
	return binPath
}
