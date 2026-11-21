package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Gateway is a background control plane started by a sandbox.
type Gateway struct {
	Sandbox *Sandbox
	State   State
}

// Status is `niq status --json`. The shapes are duplicated here on purpose:
// a black-box suite asserts the documented wire contract, so a change to the
// internal structs must not silently change the test.
type Status struct {
	Version string `json:"version"`
	Gateway struct {
		Running bool   `json:"running"`
		PID     int    `json:"pid"`
		Addr    string `json:"addr"`
		URL     string `json:"url"`
		Log     string `json:"log"`
		Started string `json:"started"`
	} `json:"gateway"`
	Projects []ProjectStatus `json:"projects"`
}

// ProjectStatus is one row of the same snapshot.
type ProjectStatus struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
	WebUI   int    `json:"webui"`
	Bus     int    `json:"bus"`
	URL     string `json:"url"`
	Log     string `json:"log"`
}

// Start boots a background gateway on an ephemeral port. It is the default in
// every test: a fixed port would collide with whatever else is running on the
// machine, including another suite running in parallel.
func (s *Sandbox) Start(t *testing.T) *Gateway {
	t.Helper()
	return s.startGateway(t, "127.0.0.1:0")
}

// StartOn boots it on a specific loopback port, for the tests that are about
// that port (conflicts, restart).
func (s *Sandbox) StartOn(t *testing.T, port int) *Gateway {
	t.Helper()
	return s.startGateway(t, fmt.Sprintf("127.0.0.1:%d", port))
}

func (s *Sandbox) startGateway(t *testing.T, addr string) *Gateway {
	t.Helper()
	// A background start returns only once the child is serving, so no polling
	// is needed here: exit 0 already means the gateway answers. The sandbox's
	// own cleanup stops it, whether the test started it or a command did.
	s.Run("--addr", addr).RequireExit(t, 0)
	return &Gateway{Sandbox: s, State: s.RequireState()}
}

// URL is the address that reaches this gateway from the same machine.
func (g *Gateway) URL() string {
	host, port, err := net.SplitHostPort(g.State.Bound)
	if err != nil {
		return "http://" + g.State.Bound
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Port is the port the gateway actually bound.
func (g *Gateway) Port() int {
	_, port, err := net.SplitHostPort(g.State.Bound)
	if err != nil {
		return 0
	}
	var n int
	fmt.Sscanf(port, "%d", &n)
	return n
}

// Status runs `niq status --json` and decodes it.
func (g *Gateway) Status(t *testing.T) Status {
	t.Helper()
	res := g.Sandbox.Run("status", "--json").RequireExit(t, 0)
	var st Status
	if err := json.Unmarshal([]byte(res.Stdout), &st); err != nil {
		t.Fatalf("decode status --json: %v\n%s", err, res)
	}
	return st
}

// Project returns one entry of the snapshot.
func (g *Gateway) Project(t *testing.T, id string) (ProjectStatus, bool) {
	t.Helper()
	for _, p := range g.Status(t).Projects {
		if p.ID == id {
			return p, true
		}
	}
	return ProjectStatus{}, false
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

// Get performs a request against the gateway.
func (g *Gateway) Get(t *testing.T, path string) (int, string) {
	t.Helper()
	return g.do(t, http.MethodGet, path, nil, "")
}

// GetHTML is a navigation, as a browser makes it: the gateway answers a page
// rather than a JSON body.
func (g *Gateway) GetHTML(t *testing.T, path string) (int, string) {
	t.Helper()
	return g.do(t, http.MethodGet, path, nil, "text/html")
}

// PostJSON performs a JSON POST.
func (g *Gateway) PostJSON(t *testing.T, path string, body any) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return g.do(t, http.MethodPost, path, bytes.NewReader(raw), "application/json")
}

func (g *Gateway) do(t *testing.T, method, path string, body io.Reader, accept string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, g.URL()+path, body)
	if err != nil {
		t.Fatalf("build request %s %s: %v", method, path, err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if body != nil && accept == "application/json" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(raw)
}

// WaitServing waits until the gateway answers /api/projects. Only a foreground
// gateway needs it: a background start has already waited for this.
func (g *Gateway) WaitServing(t *testing.T) {
	t.Helper()
	WaitFor(t, "gateway serving "+g.URL(), 20*time.Second, func() bool {
		req, err := http.NewRequest(http.MethodGet, g.URL()+"/api/projects", nil)
		if err != nil {
			return false
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode == http.StatusOK
	})
}

// Foreground is a `niq -f` child the test drives directly: it is the mode a
// supervisor runs, so it is worth exercising with real signals.
type Foreground struct {
	s       *Sandbox
	cmd     *exec.Cmd
	outPath string
	errPath string
	done    chan error
}

// StartForeground launches the CLI attached to pipes and returns immediately.
func (s *Sandbox) StartForeground(t *testing.T, args ...string) *Foreground {
	t.Helper()
	dir := t.TempDir()
	f := &Foreground{s: s, outPath: filepath.Join(dir, "stdout"), errPath: filepath.Join(dir, "stderr")}

	out, err := os.Create(f.outPath)
	if err != nil {
		t.Fatalf("foreground stdout: %v", err)
	}
	errOut, err := os.Create(f.errPath)
	if err != nil {
		t.Fatalf("foreground stderr: %v", err)
	}

	f.cmd = exec.Command(Bin(t), args...)
	f.cmd.Dir = s.Home
	f.cmd.Env = s.env
	f.cmd.Stdout, f.cmd.Stderr = out, errOut
	if err := f.cmd.Start(); err != nil {
		t.Fatalf("start foreground: %v", err)
	}
	f.done = make(chan error, 1)
	go func() {
		err := f.cmd.Wait()
		out.Close()
		errOut.Close()
		f.done <- err
	}()
	t.Cleanup(func() {
		_ = f.cmd.Process.Kill()
	})
	return f
}

// Signal sends a signal to the foreground process.
func (f *Foreground) Signal(t *testing.T, sig os.Signal) {
	t.Helper()
	if err := f.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal foreground: %v", err)
	}
}

// Wait blocks until the process exits and returns its code.
func (f *Foreground) Wait(t *testing.T) int {
	t.Helper()
	select {
	case <-time.After(20 * time.Second):
		t.Fatalf("foreground process did not exit within 20s\n%s", f.Output())
		return -1
	case err := <-f.done:
		if err == nil {
			return 0
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		t.Fatalf("foreground wait: %v", err)
		return -1
	}
}

// Output is what the process printed so far (or in total, once it exited).
func (f *Foreground) Output() string {
	out, _ := os.ReadFile(f.outPath)
	errOut, _ := os.ReadFile(f.errPath)
	return fmt.Sprintf("--- stdout ---\n%s\n--- stderr ---\n%s", out, errOut)
}
