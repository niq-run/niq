// Project WebUI proxying: the control plane is the only port exposed to the
// outside world, so a project's WebUI is reached through it at /p/<id>/ rather
// than on the project's own (loopback-only) port. One origin for the whole
// product also means one basic-auth prompt and no CORS.
package control

import (
	"context"
	"fmt"
	"log"
	stdhttp "net/http"
	"net/http/httputil"
	"net/url"
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
func newProjectProxy() *httputil.ReverseProxy {
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
			log.Printf("[control] proxy %s p=%s %s: %v", t.addr, t.id, r.URL.RequestURI(), err)
			// A navigation goes to the projects list with a start prompt (the SPA
			// owns that UI); the SPA's own fetch/stream calls keep the plain 502
			// they know how to report.
			if wantsHTML(r) {
				redirectToStartPrompt(w, r, t.id)
				return
			}
			stdhttp.Error(w, fmt.Sprintf("project WebUI at %s is unreachable: %v", t.addr, err), stdhttp.StatusBadGateway)
		},
	}
}

// wantsHTML reports whether the request is a browser navigation, as opposed to
// the SPA's own fetch/EventSource calls, which want the plain error.
func wantsHTML(r *stdhttp.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// redirectToStartPrompt sends a browser that opened a stopped project's address
// to the control plane's project list, where the SPA asks whether to start it.
// The list stays visible behind the prompt, so dismissing it still lands the
// user somewhere useful — and the prompt is drawn by the same UI as everything
// else instead of being a second, server-rendered page.
func redirectToStartPrompt(w stdhttp.ResponseWriter, r *stdhttp.Request, id string) {
	stdhttp.Redirect(w, r, "/?start="+url.QueryEscape(id), stdhttp.StatusFound)
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
		// Never started, so there is no address to proxy to — same prompt, which
		// is exactly the "click the link, start the project" flow.
		if wantsHTML(r) {
			redirectToStartPrompt(w, r, id)
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
