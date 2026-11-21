//go:build e2e

package cli_e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EDynamicPortIsChosenByTheOS pins the two-address contract: the record
// keeps what was asked for (so a restart reuses it) and what was bound (so the
// URL and liveness checks know where to look).
func TestE2EDynamicPortIsChosenByTheOS(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	g := s.Start(t)

	if g.State.Addr != "127.0.0.1:0" {
		t.Fatalf("addr = %q, want 127.0.0.1:0", g.State.Addr)
	}
	if g.Port() <= 0 {
		t.Fatalf("bound = %q, want a real port", g.State.Bound)
	}
	snap := g.Status(t)
	if !strings.HasSuffix(snap.Gateway.URL, fmt.Sprintf(":%d", g.Port())) {
		t.Fatalf("url = %q, want the bound port %d", snap.Gateway.URL, g.Port())
	}
}

// TestE2EExplicitPortIsHonored is the deployment default: a fixed address.
func TestE2EExplicitPortIsHonored(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	port := harness.FreePort(t)
	g := s.StartOn(t, port)

	want := fmt.Sprintf("127.0.0.1:%d", port)
	if g.State.Bound != want {
		t.Fatalf("bound = %q, want %q", g.State.Bound, want)
	}
	if code, _ := g.Get(t, "/api/projects"); code != 200 {
		t.Fatalf("GET /api/projects = %d", code)
	}
}

// TestE2EInvalidAddressFailsWithoutState: a bind that cannot work is a startup
// failure, and a failed startup must not leave a record that reads as running.
func TestE2EInvalidAddressFailsWithoutState(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	res := s.Run("--addr", "nonsense").RequireExit(t, 1)
	res.RequireStderrContains(t, "error:")
	if _, ok := s.State(); ok {
		t.Fatalf("a failed start left a record: %+v", s.RequireState())
	}
}

// TestE2EPortConflictNamesTheHolder is the non-interactive half of the conflict
// flow: a script gets the holder and the two ways out instead of a bind error.
func TestE2EPortConflictNamesTheHolder(t *testing.T) {
	t.Parallel()
	first := harness.NewSandbox(t)
	port := harness.FreePort(t)
	g := first.StartOn(t, port)

	second := harness.NewSandbox(t)
	res := second.Run("--addr", fmt.Sprintf("127.0.0.1:%d", port))
	if res.Exit == 0 {
		t.Fatalf("a second gateway started on a taken port\n%s", res)
	}
	res.RequireStderrContains(t,
		"already in use by pid",
		fmt.Sprintf("pid %d", g.State.PID),
		"--addr",
	)

	// The loser must not have written over the winner's record.
	if _, ok := second.State(); ok {
		t.Fatalf("the losing start wrote a record: %+v", second.RequireState())
	}
	if code, _ := g.Get(t, "/api/projects"); code != 200 {
		t.Fatalf("the first gateway stopped serving: %d", code)
	}
}

// TestE2EDynamicPortsNeverConflict: :0 is how two installations coexist, so it
// must skip the conflict path entirely.
func TestE2EDynamicPortsNeverConflict(t *testing.T) {
	t.Parallel()
	a, b := harness.NewSandbox(t), harness.NewSandbox(t)
	ga, gb := a.Start(t), b.Start(t)

	if ga.Port() == gb.Port() {
		t.Fatalf("both gateways bound %d", ga.Port())
	}
	if code, _ := ga.Get(t, "/api/projects"); code != 200 {
		t.Fatalf("first gateway = %d", code)
	}
	if code, _ := gb.Get(t, "/api/projects"); code != 200 {
		t.Fatalf("second gateway = %d", code)
	}
}
