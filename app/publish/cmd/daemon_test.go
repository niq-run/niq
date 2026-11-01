package main

import (
	"os"
	"testing"

	"github.com/niq-run/niq/app/niqhome"
)

func TestProbeAddr(t *testing.T) {
	cases := map[string]string{
		":9527":          "127.0.0.1:9527",
		"0.0.0.0:9527":   "127.0.0.1:9527",
		"[::]:9527":      "127.0.0.1:9527",
		"127.0.0.1:8080": "127.0.0.1:8080",
		"10.0.0.2:80":    "10.0.0.2:80",
		"nonsense":       "",
	}
	for in, want := range cases {
		if got := probeAddr(in); got != want {
			t.Errorf("probeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestControlStateRoundTrip covers the record start/stop/status share: it must
// survive a write and only be cleared by its owner.
func TestControlStateRoundTrip(t *testing.T) {
	t.Setenv(niqhome.Env, t.TempDir())

	if _, ok := readControlState(); ok {
		t.Fatal("empty root reported a running control plane")
	}
	st := &controlState{
		PID:     os.Getpid(),
		Addr:    ":9527",
		Bound:   "[::]:9527",
		Started: "2026-01-01T00:00:00Z",
	}
	writeControlState(st)
	got, ok := readControlState()
	if !ok {
		t.Fatal("state not readable after write")
	}
	if got.PID != st.PID || got.Addr != st.Addr || got.Bound != st.Bound {
		t.Fatalf("state round-trip = %+v, want %+v", got, st)
	}

	clearControlState(st.PID + 1) // a losing instance must not clear it
	if _, ok := readControlState(); !ok {
		t.Fatal("cleared by a process that does not own the record")
	}
	clearControlState(st.PID)
	if _, ok := readControlState(); ok {
		t.Fatal("owner did not clear the record")
	}
}
