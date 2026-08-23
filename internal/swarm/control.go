// Control-layer HTTP service: the fixed-port (default :9527) control plane a
// user opens when no project is attached. It serves the same SPA in "control"
// mode — project management only — and can start a project, which runs as its
// own process on its own dynamically-assigned bus/WebUI ports; the user is then
// redirected to the project's WebUI.
package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/54c1/niq/internal/swarm/webui"
)

// ControlOptions configures the control-plane service.
type ControlOptions struct {
	Addr string // default ":9527"
}

// RunControl runs the control-plane service until ctx is cancelled (e.g. Ctrl+C).
func RunControl(opts ControlOptions) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return NewControl(opts.Addr).Start(ctx)
}

// Control is the control-plane HTTP service.
type Control struct {
	addr       string
	server     *stdhttp.Server
	listener   net.Listener
	bound      string
	controlURL string
}

// NewControl creates a control-plane service bound to addr (":9527" when empty).
func NewControl(addr string) *Control {
	if addr == "" {
		addr = ":9527"
	}
	return &Control{addr: addr, controlURL: "http://localhost" + addr}
}

// Bind binds the listen socket and returns the resolved host:port.
func (c *Control) Bind() (string, error) {
	if c.listener != nil {
		return c.bound, nil
	}
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		return "", fmt.Errorf("control: bind %s: %w", c.addr, err)
	}
	c.listener = ln
	c.bound = ln.Addr().String()
	return c.bound, nil
}

// ResolvedAddr returns the bound address (empty until Bind).
func (c *Control) ResolvedAddr() string { return c.bound }

// Start binds (if needed) and serves, blocking until ctx is cancelled.
func (c *Control) Start(ctx context.Context) error {
	if c.listener == nil {
		if _, err := c.Bind(); err != nil {
			return err
		}
	}
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /api/context", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		json.NewEncoder(w).Encode(webui.ContextInfo{Mode: "control", ControlURL: c.controlURL})
	})
	mux.HandleFunc("GET /api/projects", c.handleListProjects)
	mux.HandleFunc("POST /api/projects/{id}/start", c.handleStartProject)

	assets, err := webui.AssetsFS()
	if err != nil {
		return fmt.Errorf("control: assets: %w", err)
	}
	mux.Handle("GET /", stdhttp.FileServer(stdhttp.FS(assets)))

	c.server = &stdhttp.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.server.Shutdown(shutdownCtx)
	}()
	log.Printf("[control] listening on %s", c.bound)
	fmt.Printf("niq control listening at %s\n", localhostURL(c.bound))
	if err := c.server.Serve(c.listener); err != nil && err != stdhttp.ErrServerClosed {
		return fmt.Errorf("control: %w", err)
	}
	return nil
}

// handleListProjects returns the project definitions.
func (c *Control) handleListProjects(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	projects, err := ListProjects()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(projects)
}

// handleStartProject launches a project as its own process on dynamic ports and
// returns the resolved WebUI URL for the user to be redirected to.
func (c *Control) handleStartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		stdhttp.Error(w, "control: resolve executable: "+err.Error(), 500)
		return
	}
	cmd := exec.Command(exe, "project", "run", id, "--bus", ":0", "--webui", ":0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		stdhttp.Error(w, "control: start project: "+err.Error(), 500)
		return
	}
	// Best-effort watchdog: reap the subprocess so it doesn't zombie on exit.
	go cmd.Wait()

	// Poll project.json for the dynamically-assigned ports.
	webUI := resolvedWebUI(id)
	json.NewEncoder(w).Encode(map[string]any{
		"project":    id,
		"webui_url":  localhostURL(webUI),
		"webui_port": portOf(webUI),
		"bus_port":   resolvedBus(id),
	})
}

// resolvedWebUI polls a project's persisted ports until the WebUI port is
// assigned or the deadline passes.
func resolvedWebUI(id string) string {
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := LoadProject(id); err == nil && p.Ports.WebUI != 0 {
			return "127.0.0.1:" + strconv.Itoa(p.Ports.WebUI)
		}
		time.Sleep(150 * time.Millisecond)
	}
	return ""
}

func resolvedBus(id string) int {
	if p, err := LoadProject(id); err == nil {
		return p.Ports.Bus
	}
	return 0
}