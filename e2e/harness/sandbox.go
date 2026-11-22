package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Result is one finished `niq` invocation.
type Result struct {
	Stdout string
	Stderr string
	Exit   int
}

// String renders the whole invocation for a failure message: a failing
// assertion should never need a rerun to say what the command printed.
func (r Result) String() string {
	return fmt.Sprintf("exit=%d\n--- stdout ---\n%s\n--- stderr ---\n%s", r.Exit, r.Stdout, r.Stderr)
}

// RequireExit fails the test unless the command exited with want.
func (r Result) RequireExit(t *testing.T, want int) Result {
	t.Helper()
	if r.Exit != want {
		t.Fatalf("exit = %d, want %d\n%s", r.Exit, want, r)
	}
	return r
}

// RequireContains fails unless every needle appears in stdout.
func (r Result) RequireContains(t *testing.T, needles ...string) Result {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(r.Stdout, n) {
			t.Fatalf("stdout does not contain %q\n%s", n, r)
		}
	}
	return r
}

// RequireStderrContains fails unless every needle appears in stderr.
func (r Result) RequireStderrContains(t *testing.T, needles ...string) Result {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(r.Stderr, n) {
			t.Fatalf("stderr does not contain %q\n%s", n, r)
		}
	}
	return r
}

// State is ~/.niq/shell.json: the gateway's own record of itself.
type State struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`
	Bound   string `json:"bound"`
	Started string `json:"started"`
}

// Sandbox is one isolated niq installation: its own NIQ_HOME, its own HOME,
// and a PATH whose `open`/`npm` do nothing. Every test gets one, so a suite can
// run in parallel against a machine that is already using niq.
type Sandbox struct {
	t     *testing.T
	Home  string // NIQ_HOME
	Dir   string // HOME (and XDG_CONFIG_HOME's parent)
	Stubs string // PATH stubs: open, xdg-open, npm

	env []string
}

// NewSandbox creates the isolated installation and registers its cleanup.
func NewSandbox(t *testing.T) *Sandbox {
	t.Helper()
	base := t.TempDir()
	s := &Sandbox{
		t:     t,
		Home:  filepath.Join(base, "niq-home"),
		Dir:   filepath.Join(base, "home"),
		Stubs: filepath.Join(base, "stubs"),
	}
	for _, d := range []string{s.Home, s.Dir, filepath.Join(s.Home, "logs"), s.Stubs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("sandbox: mkdir %s: %v", d, err)
		}
	}
	s.writeStubs()
	s.env = []string{
		"NIQ_HOME=" + s.Home,
		"HOME=" + s.Dir,
		"XDG_CONFIG_HOME=" + filepath.Join(s.Dir, ".config"),
		// No passive update check: a suite must never touch the network.
		"NIQ_NO_UPDATE_CHECK=1",
		// No colour escapes, so assertions can compare the text directly.
		"TERM=dumb",
		"SHELL=/bin/sh",
		// Stubs first: `niq w` must not launch a browser and `niq update`
		// must not reach npm.
		"PATH=" + s.Stubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"NIQ_E2E_LOG=" + s.logDir(),
	}
	// The sandbox owns every process its commands start, not just the ones a
	// test started through Start: `niq i` and `niq w` bring a gateway up on
	// their own, and a failed assertion must not leave it running.
	t.Cleanup(s.stopEverything)
	return s
}

// stopEverything is the sandbox's last word: stop what it started, and make
// sure the record's process is gone even if the graceful path failed.
func (s *Sandbox) stopEverything() {
	st, had := s.State()
	s.Run("stop", "-p")
	if had && ProcessAlive(st.PID) {
		if p, err := os.FindProcess(st.PID); err == nil {
			_ = p.Kill()
		}
	}
}

// Env is what every invocation runs with.
func (s *Sandbox) Env() []string { return s.env }

// logDir is where the PATH stubs record their calls.
func (s *Sandbox) logDir() string { return filepath.Join(s.Home, "logs") }

// StubLog returns the arguments recorded by the named stub, one call per line.
func (s *Sandbox) StubLog(name string) string {
	b, err := os.ReadFile(filepath.Join(s.logDir(), name+".log"))
	if err != nil {
		return ""
	}
	return string(b)
}

// writeStubs installs the commands a test must not really run.
func (s *Sandbox) writeStubs() {
	stubs := map[string]string{
		// `niq w` opens the browser best-effort; record the URL instead.
		"open":     "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NIQ_E2E_LOG/open.log\"\n",
		"xdg-open": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NIQ_E2E_LOG/open.log\"\n",
		// `niq update` shells out to npm. It is out of scope for the suites and
		// must fail fast rather than download anything.
		"npm": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$NIQ_E2E_LOG/npm.log\"\nexit 1\n",
	}
	for name, body := range stubs {
		path := filepath.Join(s.Stubs, name)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			s.t.Fatalf("sandbox: write stub %s: %v", name, err)
		}
	}
}

// Run invokes the CLI with the sandbox's environment and no stdin.
func (s *Sandbox) Run(args ...string) Result { return s.RunStdin("", args...) }

// RunStdin invokes the CLI with the given stdin. An empty string is still a
// pipe, which is the "no terminal" case the CLI must handle gracefully.
func (s *Sandbox) RunStdin(stdin string, args ...string) Result {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, Bin(s.t), args...)
	cmd.Dir = s.Home
	cmd.Env = s.env
	cmd.Stdin = strings.NewReader(stdin)

	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()

	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			s.t.Fatalf("run niq %v: %v", args, err)
		}
		res.Exit = exit.ExitCode()
	}
	return res
}

// State reads the gateway's record, if it has written one.
func (s *Sandbox) State() (State, bool) {
	b, err := os.ReadFile(filepath.Join(s.Home, "shell.json"))
	if err != nil {
		return State{}, false
	}
	var st State
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 {
		return State{}, false
	}
	return st, true
}

// RequireState fails unless a record exists, and returns it.
func (s *Sandbox) RequireState() State {
	s.t.Helper()
	st, ok := s.State()
	if !ok {
		s.t.Fatal("no shell.json: the gateway did not record itself")
	}
	return st
}

// RequireNoState fails when a record is left behind.
func (s *Sandbox) RequireNoState() {
	s.t.Helper()
	if _, ok := s.State(); ok {
		s.t.Fatalf("shell.json still present: %+v", s.mustState())
	}
}

func (s *Sandbox) mustState() State {
	st, _ := s.State()
	return st
}

// ReadFile reads a file under NIQ_HOME.
func (s *Sandbox) ReadFile(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(s.Home, rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// WriteFile writes a file under NIQ_HOME, creating parent directories.
func (s *Sandbox) WriteFile(rel, content string) {
	s.t.Helper()
	path := filepath.Join(s.Home, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.t.Fatalf("write %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		s.t.Fatalf("write %s: %v", rel, err)
	}
}

// FreePort reserves a port and gives it back. It is the one place a test needs
// a specific number (the conflict cases); everywhere else uses :0.
func FreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer ln.Close()
	_, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	return port
}

// ProcessAlive reports whether pid is still running.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}

// WaitFor polls cond until it returns true or the deadline passes.
func WaitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}
