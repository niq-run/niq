// Gateway-plane HTTP service: the fixed-port (default 127.0.0.1:9527) control
// plane a user opens when no project is attached. It serves the same SPA in
// "control" mode — project management only — and can start a project, which
// runs as its own process on its own dynamically-assigned bus/WebUI ports. The
// SPA then navigates to the project's UI at /p/{id}/: there is no redirect, the
// bundle it already has does the routing.
//
// Project instances are not children of this process in any way that matters:
// an instance is identified by the ports persisted in its project.json, so one
// the plane launched, one started by hand and one that outlived a previous
// plane are the same thing. Every lifecycle action goes through those ports.
//
// File layout: the service shell + routing live here; project lifecycle
// handlers are in projects.go and template CRUD is in templates.go.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/niq-run/niq/app/standalone/store"
	"github.com/niq-run/niq/app/standalone/template"
)

// Options configures the control-plane service.
type Options struct {
	Addr string // default "127.0.0.1:9527"; pass an explicit addr to accept remote connections
	Auth string // optional user:pass basic-auth spec (protects non-localhost access; empty disables auth)
	// UI lists the static interfaces this gateway serves, most specific prefix
	// first is not required. The gateway knows nothing about them beyond the
	// filesystem; the composition root decides what the product ships.
	UI []UIMount
	// OnReady, when set, runs once the listen socket is bound and before
	// serving starts. It receives the resolved address (e.g. "[::]:9527") so a
	// launcher can record where the daemon actually landed instead of guessing
	// the port from the requested address.
	OnReady func(resolved string)
}

// Run runs the control-plane service until ctx is cancelled (e.g. Ctrl+C).
func Run(opts Options) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Make sure the built-in project templates are on disk so the new-project
	// dropdown has something to offer on first run.
	if err := template.Seed(template.TemplatesDir()); err != nil {
		log.Printf("[gateway] seed templates: %v", err)
	}
	c := New(opts.Addr)
	for _, m := range opts.UI {
		c.Mount(m)
	}
	if user, pass, enabled, warning := StartupAuth(opts.Addr, opts.Auth, store.AuthConfigPath()); enabled {
		c.SetBasicAuth(user, pass)
		log.Printf("[gateway] WebUI remote access protected with basic auth (localhost stays open)")
	} else if warning != "" {
		// Auth is opt-in. Say clearly that an exposed bind is unauthenticated, and
		// also put it in the service log: a background start has no stderr to show.
		log.Printf("[gateway] %s", warning)
		fmt.Fprintln(os.Stderr, warning)
	}
	// Bind here rather than inside Start so OnReady can report the resolved
	// address; Start reuses the already-bound listener.
	if _, err := c.Bind(); err != nil {
		return err
	}
	if opts.OnReady != nil {
		opts.OnReady(c.ResolvedAddr())
	}
	return c.Start(ctx)
}

// Gateway is the control-plane HTTP service.
type Gateway struct {
	addr     string
	server   *stdhttp.Server
	listener net.Listener
	bound    string

	// proxy serves /p/<id>/* from each project's own loopback WebUI.
	proxy *httputil.ReverseProxy

	// ui is the mount table filled by the composition root (see Mount).
	ui []UIMount

	authUser string // basic-auth user for non-loopback access ("" = disabled)
	authPass string
}

// New creates a control-plane service bound to addr ("127.0.0.1:9527"
// when empty). The default is loopback so a credential-less server stays on this
// machine; passing an explicit addr is what exposes it.
func New(addr string) *Gateway {
	if addr == "" {
		addr = "127.0.0.1:9527"
	}
	c := &Gateway{addr: addr}
	c.proxy = c.newProjectProxy()
	return c
}

// SetBasicAuth enables HTTP basic auth for requests from non-loopback peers
// (local machine access stays password-free). Cleared when either user or pass
// is empty.
func (c *Gateway) SetBasicAuth(user, pass string) {
	c.authUser, c.authPass = user, pass
}

// PortInUseError reports that the listen address is already taken. The
// gateway only detects it; choosing what to do (stop the holder, start on
// another port) belongs to the layer that owns the daemon lifecycle, so this
// type keeps the fact separable from the policy.
type PortInUseError struct {
	Addr string
	Err  error
}

func (e *PortInUseError) Error() string {
	return fmt.Sprintf("gateway: %s is already in use", e.Addr)
}

func (e *PortInUseError) Unwrap() error { return e.Err }

// IsPortInUse reports whether err means "this address is already taken":
// either the typed error Bind returns, or a raw EADDRINUSE from elsewhere.
func IsPortInUse(err error) bool {
	var inUse *PortInUseError
	return errors.As(err, &inUse) || errors.Is(err, syscall.EADDRINUSE)
}

// Bind binds the listen socket and returns the resolved host:port.
func (c *Gateway) Bind() (string, error) {
	if c.listener != nil {
		return c.bound, nil
	}
	ln, err := net.Listen("tcp", c.addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return "", &PortInUseError{Addr: c.addr, Err: err}
		}
		return "", fmt.Errorf("gateway: bind %s: %w", c.addr, err)
	}
	c.listener = ln
	c.bound = ln.Addr().String()
	return c.bound, nil
}

// ResolvedAddr returns the bound address (empty until Bind).
func (c *Gateway) ResolvedAddr() string { return c.bound }

// Start binds (if needed) and serves, blocking until ctx is cancelled.
func (c *Gateway) Start(ctx context.Context) error {
	if c.listener == nil {
		if _, err := c.Bind(); err != nil {
			return err
		}
	}
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /api/context", func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		json.NewEncoder(w).Encode(ContextInfo{Mode: "control"})
	})
	mux.HandleFunc("GET /api/templates", c.handleListTemplates)
	mux.HandleFunc("GET /api/templates/{name}", c.handleTemplateDetail)
	mux.HandleFunc("POST /api/templates", c.handleCreateTemplate)
	mux.HandleFunc("POST /api/templates/import", c.handleImportTemplate)
	mux.HandleFunc("PUT /api/templates/{name}", c.handleUpdateTemplate)
	mux.HandleFunc("DELETE /api/templates/{name}", c.handleDeleteTemplate)
	mux.HandleFunc("GET /api/projects", c.handleListProjects)
	mux.HandleFunc("GET /api/projects/dirs", c.handleListDirs)
	mux.HandleFunc("POST /api/projects/link", c.handleLinkProject)
	mux.HandleFunc("GET /api/providers", c.handleGetProviders)
	mux.HandleFunc("PUT /api/providers", c.handleUpdateProviders)
	mux.HandleFunc("GET /api/projects/{id}/template-preview", c.handleTemplatePreview)
	mux.HandleFunc("POST /api/projects", c.handleCreateProject)
	mux.HandleFunc("POST /api/projects/{id}/start", c.handleStartProject)
	mux.HandleFunc("POST /api/projects/{id}/stop", c.handleStopProject)
	mux.HandleFunc("POST /api/projects/{id}/restart", c.handleRestartProject)

	// Project WebUIs are reached through the control plane (the only exposed
	// port): /p/<id>/... is served from that project's own loopback WebUI.
	mux.HandleFunc(projectPrefix+"{id}/", c.handleProjectProxy)

	// Registered without a method so it cannot conflict with the all-methods
	// /p/{id}/ proxy above ("GET /" vs "/p/{id}/" is ambiguous: the former has a
	// narrower method but a wider path).
	mux.HandleFunc("/", c.handleUI)

	c.server = &stdhttp.Server{Handler: cors(BasicAuth(c.authUser, c.authPass, mux))}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.server.Shutdown(shutdownCtx)
	}()
	log.Printf("[gateway] listening on %s", c.bound)
	fmt.Printf("niq gateway listening at %s\n", store.LocalhostURL(c.bound))
	if err := c.server.Serve(c.listener); err != nil && err != stdhttp.ErrServerClosed {
		return fmt.Errorf("gateway: %w", err)
	}
	return nil
}

// cors allows a project WebUI (cross-origin, on its own port) to call the
// control plane's projects/start APIs.
func cors(next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(stdhttp.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
