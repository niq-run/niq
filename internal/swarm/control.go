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
	// Make sure the built-in project templates are on disk so the new-project
	// dropdown has something to offer on first run.
	if err := SeedTemplates(TemplatesDir()); err != nil {
		log.Printf("[control] seed templates: %v", err)
	}
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
	mux.HandleFunc("GET /api/templates", c.handleListTemplates)
	mux.HandleFunc("GET /api/projects", c.handleListProjects)
	mux.HandleFunc("POST /api/projects", c.handleCreateProject)
	mux.HandleFunc("POST /api/projects/{id}/start", c.handleStartProject)

	assets, err := webui.AssetsFS()
	if err != nil {
		return fmt.Errorf("control: assets: %w", err)
	}
	mux.Handle("GET /", stdhttp.FileServer(stdhttp.FS(assets)))

	c.server = &stdhttp.Server{Handler: corsControl(mux)}
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

// handleListTemplates returns the available project template names for the
// new-project dropdown.
func (c *Control) handleListTemplates(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	names, err := ListTemplates()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(names)
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

// handleCreateProject creates a project from a named template and immediately
// starts it, returning the resolved WebUI URL.
func (c *Control) handleCreateProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		ID       string `json:"id"`
		Template string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		stdhttp.Error(w, "id is required", 400)
		return
	}
	if body.Template == "" {
		body.Template = "dev"
	}
	tmpl, err := LoadTemplate(TemplatesDir(), body.Template)
	if err != nil {
		stdhttp.Error(w, "unknown template: "+body.Template, 400)
		return
	}
	if _, err := CreateProject(body.ID, tmpl); err != nil {
		stdhttp.Error(w, err.Error(), 409)
		return
	}
	c.launchProject(w, body.ID)
}

// handleStartProject launches a project as its own process on dynamic ports and
// returns the resolved WebUI URL for the user to be redirected to.
func (c *Control) handleStartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	c.launchProject(w, id)
}

// launchProject spawns `niq project run <id>` on dynamic ports and answers with
// the project's resolved WebUI URL.
func (c *Control) launchProject(w stdhttp.ResponseWriter, id string) {
	exe, err := os.Executable()
	if err != nil {
		stdhttp.Error(w, "control: resolve executable: "+err.Error(), 500)
		return
	}
	// No --bus/--webui: Reuse the persisted (already-assigned) ports so a
	// re-started project lands on the same addresses instead of jumping.
	cmd := exec.Command(exe, "project", "run", id)
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

// corsControl allows a project WebUI (cross-origin, on its own port) to call the
// control plane's projects/start APIs.
func corsControl(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(stdhttp.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
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