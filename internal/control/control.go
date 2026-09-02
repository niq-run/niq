// Control-plane HTTP service: the fixed-port (default :9527) control plane a
// user opens when no project is attached. It serves the same SPA in "control"
// mode — project management only — and can start a project, which runs as its
// own process on its own dynamically-assigned bus/WebUI ports; the user is then
// redirected to the project's WebUI.
//
// File layout: the service shell + routing live here; project lifecycle
// handlers are in projects.go and template CRUD is in templates.go.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/niq-run/niq/internal/project"
	"github.com/niq-run/niq/internal/webui"
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
	if err := project.SeedTemplates(project.TemplatesDir()); err != nil {
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

	mu    sync.Mutex
	procs map[string]*os.Process // project id -> the running 'niq project run' process
}

// NewControl creates a control-plane service bound to addr (":9527" when empty).
func NewControl(addr string) *Control {
	if addr == "" {
		addr = ":9527"
	}
	return &Control{addr: addr, controlURL: "http://localhost" + addr, procs: map[string]*os.Process{}}
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
	mux.HandleFunc("GET /api/templates/{name}", c.handleTemplateDetail)
	mux.HandleFunc("POST /api/templates", c.handleCreateTemplate)
	mux.HandleFunc("DELETE /api/templates/{name}", c.handleDeleteTemplate)
	mux.HandleFunc("GET /api/projects", c.handleListProjects)
	mux.HandleFunc("POST /api/projects", c.handleCreateProject)
	mux.HandleFunc("POST /api/projects/{id}/start", c.handleStartProject)
	mux.HandleFunc("POST /api/projects/{id}/stop", c.handleStopProject)
	mux.HandleFunc("POST /api/projects/{id}/restart", c.handleRestartProject)

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
	fmt.Printf("niq control listening at %s\n", project.LocalhostURL(c.bound))
	if err := c.server.Serve(c.listener); err != nil && err != stdhttp.ErrServerClosed {
		return fmt.Errorf("control: %w", err)
	}
	return nil
}

// corsControl allows a project WebUI (cross-origin, on its own port) to call the
// control plane's projects/start APIs.
func corsControl(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(stdhttp.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
