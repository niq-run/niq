//go:build e2e

package cli_e2e

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/e2e/harness"
)

// TestE2EStartRecordsTheGateway covers the contract every other command relies
// on: a background start returns only once the gateway serves, and the record
// it leaves says how to reach it.
func TestE2EStartRecordsTheGateway(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	g := s.Start(t)

	st := g.State
	if st.Addr != "127.0.0.1:0" {
		t.Fatalf("addr = %q, want the address that was asked for", st.Addr)
	}
	if st.Bound == "" || strings.HasSuffix(st.Bound, ":0") {
		t.Fatalf("bound = %q, want the port the OS chose", st.Bound)
	}

	snap := g.Status(t)
	if !snap.Gateway.Running || snap.Gateway.PID != st.PID {
		t.Fatalf("status gateway = %+v, want running with pid %d", snap.Gateway, st.PID)
	}
	if !strings.HasSuffix(snap.Gateway.URL, fmt.Sprintf(":%d", g.Port())) {
		t.Fatalf("status url = %q, want it to reach port %d", snap.Gateway.URL, g.Port())
	}

	if code, body := g.Get(t, "/api/projects"); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("GET /api/projects = %d %q, want 200 []", code, body)
	}
}

// TestE2EStartIsIdempotent pins the "repeating the command is how you check on
// the service" rule: a second bare invocation reports, it does not start a
// second gateway.
func TestE2EStartIsIdempotent(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	g := s.Start(t)
	first := g.State.PID

	res := s.Run().RequireExit(t, 0)
	second := s.RequireState()
	if second.PID != first {
		t.Fatalf("second start changed the pid: %d -> %d", first, second.PID)
	}
	res.RequireContains(t, "gateway")
}

// TestE2EStopClearsTheRecordAndTheProcess checks both halves of "stopped":
// the record is gone and the pid is gone, so a restart cannot trip over it.
func TestE2EStopClearsTheRecordAndTheProcess(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	g := s.Start(t)
	pid, port := g.State.PID, g.Port()

	s.Run("stop").RequireExit(t, 0).RequireContains(t, "stopped")
	s.RequireNoState()

	harness.WaitFor(t, "the gateway process to exit", 10*time.Second, func() bool {
		return !harness.ProcessAlive(pid)
	})
	harness.WaitFor(t, "the port to be released", 10*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err != nil {
			return true
		}
		conn.Close()
		return false
	})
}

// TestE2EStopIsIdempotent: stopping what is not running is success, not an
// error. Scripts depend on that (stop before start, cleanup after failure).
func TestE2EStopIsIdempotent(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	s.Run("stop").RequireExit(t, 0).RequireContains(t, "not running")

	s.Start(t)
	s.Run("stop").RequireExit(t, 0)
	s.Run("stop").RequireExit(t, 0).RequireContains(t, "not running")
}

// TestE2EStatusWithoutAGateway is the other half of the same contract: status
// reports, it never starts one, and it exits 0 so a prompt can call it freely.
func TestE2EStatusWithoutAGateway(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	res := s.Run("status").RequireExit(t, 0).RequireContains(t, "stopped")
	if strings.Contains(res.Stdout, "running") {
		t.Fatalf("status claims something is running:\n%s", res)
	}
	if _, ok := s.State(); ok {
		t.Fatal("status started a gateway")
	}

	snap := decodeStatus(t, s)
	if snap.Gateway.Running {
		t.Fatalf("status --json says running: %+v", snap.Gateway)
	}
}

// TestE2EStaleRecordIsNotRunning covers a machine that crashed: the record
// outlives the process. It must read as stopped, and `stop` must clear it.
func TestE2EStaleRecordIsNotRunning(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	s.WriteFile("shell.json", `{"pid":999999,"addr":"127.0.0.1:1","bound":"127.0.0.1:1","started":"2020-01-01T00:00:00Z"}`)

	snap := decodeStatus(t, s)
	if snap.Gateway.Running {
		t.Fatalf("a dead pid reads as running: %+v", snap.Gateway)
	}

	s.Run("stop").RequireExit(t, 0).RequireContains(t, "cleared a stale record")
	s.RequireNoState()
}

// TestE2ERestartKeepsTheAddress: restart is how an upgrade takes effect, so it
// must keep the port the user chose and hand over to a new process.
func TestE2ERestartKeepsTheAddress(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	port := harness.FreePort(t)
	g := s.StartOn(t, port)
	first := g.State.PID

	s.Run("restart").RequireExit(t, 0).RequireContains(t, "gateway")

	second := s.RequireState()
	if second.PID == first {
		t.Fatalf("restart kept the old pid %d", first)
	}
	if !strings.HasSuffix(second.Bound, fmt.Sprintf(":%d", port)) {
		t.Fatalf("restart moved the port: %q, want :%d", second.Bound, port)
	}
	if code, _ := (&harness.Gateway{Sandbox: s, State: second}).Get(t, "/api/projects"); code != 200 {
		t.Fatalf("restarted gateway does not serve: %d", code)
	}
}

// TestE2EForegroundServesAndStopsOnInterrupt is the supervisor's path: `-f`
// runs attached, answers, and exits 0 on SIGINT with the record cleared.
func TestE2EForegroundServesAndStopsOnInterrupt(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	port := harness.FreePort(t)

	fg := s.StartForeground(t, "-f", "--addr", fmt.Sprintf("127.0.0.1:%d", port))

	var g *harness.Gateway
	harness.WaitFor(t, "the foreground gateway to record itself", 20*time.Second, func() bool {
		st, ok := s.State()
		if !ok {
			return false
		}
		g = &harness.Gateway{Sandbox: s, State: st}
		return true
	})
	g.WaitServing(t)

	fg.Signal(t, os.Interrupt)
	if code := fg.Wait(t); code != 0 {
		t.Fatalf("foreground exit = %d, want 0\n%s", code, fg.Output())
	}
	s.RequireNoState()
}

// TestE2EForegroundRefusesASecondGateway: two planes would fight over the one
// exposed port, so the second must say so and exit.
func TestE2EForegroundRefusesASecondGateway(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)
	g := s.Start(t)

	res := s.Run("-f", "--addr", g.State.Bound)
	if res.Exit == 0 {
		t.Fatalf("a second foreground gateway started over a running one\n%s", res)
	}
	res.RequireStderrContains(t, "already running")
}

// TestE2EWOpensTheBrowserThroughAStub keeps `niq w` honest without launching
// anything: a fresh start prints the address and hands it to the opener. (On an
// already-running gateway `niq w` reports instead of opening a second tab, so
// the stub only sees a call on the start path.)
func TestE2EWOpensTheBrowserThroughAStub(t *testing.T) {
	t.Parallel()
	s := harness.NewSandbox(t)

	s.Run("w", "--addr", "127.0.0.1:0").RequireExit(t, 0)

	st := s.RequireState()
	port := fmt.Sprintf(":%d", portOf(t, st.Bound))
	// The opener is started, not waited on: the CLI is done once it hands the
	// URL over, so the stub's write races the assertion.
	harness.WaitFor(t, "the opener to be called with the bound port", 5*time.Second, func() bool {
		return strings.Contains(s.StubLog("open"), port)
	})
}

// portOf reads the port out of a host:port record.
func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port in %q: %v", addr, err)
	}
	return port
}
