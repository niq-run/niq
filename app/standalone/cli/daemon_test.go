package cli

import (
	"os"
	"testing"

	"github.com/niq-run/niq/app/standalone/store"
)

// TestControlStateRoundTrip covers the record start/stop/status share: it must
// survive a write and only be cleared by its owner.
func TestControlStateRoundTrip(t *testing.T) {
	t.Setenv(store.Env, t.TempDir())

	if _, ok := readShellState(); ok {
		t.Fatal("empty root reported a running control plane")
	}
	st := &shellState{
		PID:     os.Getpid(),
		Addr:    ":9527",
		Bound:   "[::]:9527",
		Started: "2026-01-01T00:00:00Z",
	}
	writeShellState(st)
	got, ok := readShellState()
	if !ok {
		t.Fatal("state not readable after write")
	}
	if got.PID != st.PID || got.Addr != st.Addr || got.Bound != st.Bound {
		t.Fatalf("state round-trip = %+v, want %+v", got, st)
	}

	clearShellState(st.PID + 1) // a losing instance must not clear it
	if _, ok := readShellState(); !ok {
		t.Fatal("cleared by a process that does not own the record")
	}
	clearShellState(st.PID)
	if _, ok := readShellState(); ok {
		t.Fatal("owner did not clear the record")
	}
}
