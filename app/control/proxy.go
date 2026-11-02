// Project WebUI proxying: the control plane is the only port exposed to the
// outside world, so a project's WebUI is reached through it at /p/<id>/ rather
// than on the project's own (loopback-only) port. One origin for the whole
// product also means one basic-auth prompt and no CORS.
package control

import (
	"context"
	"fmt"
	"io/fs"
	"log"
	stdhttp "net/http"
	"net/http/httputil"
	"strconv"
	"strings"

	"github.com/niq-run/niq/app/project"
)

// projectPrefix is where a project's WebUI is mounted on the control plane:
// /p/<project-id>/... The SPA derives its own API base from this same path.
const projectPrefix = "/p/"

// proxyTarget is resolved per request and carries the project's loopback WebUI
// address plus the path prefix to strip, from the handler to the proxy Rewrite.
type proxyTarget struct {
	id     string // project id (for diagnostics)
	addr   string // 127.0.0.1:<webui port>
	prefix string // /p/<id>
}

type proxyTargetKey struct{}

// newProjectProxy builds the reverse proxy serving /p/{id}/* from the project's
// own WebUI. The upstream is taken from the request (see handleProjectProxy)
// instead of being baked in, so a project that restarted on another port is
// picked up immediately and a stopped one never hits a stale address.
func (c *Control) newProjectProxy() *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		// The project's /api/stream is SSE: flush every write so events reach
		// the browser as they happen instead of sitting in a copy buffer.
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			t, _ := pr.In.Context().Value(proxyTargetKey{}).(proxyTarget)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = t.addr
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, t.prefix)
			pr.Out.URL.RawPath = "" // RawPath would still carry the prefix
			pr.SetXForwarded()
		},
		ErrorHandler: func(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
			t, _ := r.Context().Value(proxyTargetKey{}).(proxyTarget)
			// Navigation and the bundle's own static files come from the
			// control plane's embedded SPA when the project is down, so the
			// page still renders the product UI — with its "project stopped"
			// banner and start button — instead of a dead end. The SPA's API
			// calls are not SPA files and keep the plain 502 they know how to
			// report.
			if c.serveSPA(w, r, t.prefix) {
				return
			}
			log.Printf("[control] proxy %s p=%s %s: %v", t.addr, t.id, r.URL.RequestURI(), err)
			stdhttp.Error(w, fmt.Sprintf("project WebUI at %s is unreachable: %v", t.addr, err), stdhttp.StatusBadGateway)
		},
	}
}

// wantsHTML reports whether the request is a browser navigation, as opposed to
// the SPA's own fetch/EventSource calls, which want the plain error.
func wantsHTML(r *stdhttp.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// serveSPA answers a request from the control plane's embedded SPA bundle,
// stitched under the project's /p/<id>/ prefix. It lets a stopped project's
// address still serve its page: the shell and every static file load from here
// while the project's API calls keep failing, so the UI can say the project is
// stopped and offer to start it. Returns false when the path is not a bundle
// file (any API call), leaving the caller to answer with its own error.
func (c *Control) serveSPA(w stdhttp.ResponseWriter, r *stdhttp.Request, prefix string) bool {
	if c.assets == nil {
		return false
	}
	rel := strings.TrimPrefix(r.URL.Path, prefix)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "index.html"
	}
	if _, err := fs.Stat(c.assets, rel); err != nil {
		return false
	}
	stdhttp.ServeFileFS(w, r, c.assets, rel)
	return true
}

// handleProjectProxy serves /p/{id}/* from the project's own WebUI. The target
// port comes from the project's persisted ports, so it works for a project
// started by this control plane and for one started by hand.
func (c *Control) handleProjectProxy(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	p, err := project.LoadProject(id)
	if err != nil {
		stdhttp.Error(w, "project not found: "+id, stdhttp.StatusNotFound)
		return
	}
	if p.Ports.WebUI == 0 {
		// Never started, so there is no address to proxy to. Same shell as a
		// stopped project: the page says so and offers the start button.
		if wantsHTML(r) && c.serveSPA(w, r, projectPrefix+id) {
			return
		}
		stdhttp.Error(w, "project "+id+" is not running: no WebUI port assigned yet", stdhttp.StatusBadGateway)
		return
	}
	t := proxyTarget{
		id:     id,
		addr:   "127.0.0.1:" + strconv.Itoa(p.Ports.WebUI),
		prefix: projectPrefix + id,
	}
	c.proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), proxyTargetKey{}, t)))
}
