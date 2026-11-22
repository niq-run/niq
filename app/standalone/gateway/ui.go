package gateway

import (
	"io/fs"
	stdhttp "net/http"
	"strings"
)

// UIMount attaches a static interface to the gateway under Prefix. The gateway
// treats every mount the same; which UIs exist is the distribution's decision,
// made where the gateway is constructed.
type UIMount struct {
	// Prefix is the URL subtree the UI owns, e.g. "/" or "/files/".
	Prefix string
	// FS is the built bundle, rooted at its index.html.
	FS fs.FS
}

// Mount registers a UI. Mounts added later win when their prefix is longer.
func (g *Gateway) Mount(m UIMount) {
	if m.Prefix == "" {
		m.Prefix = "/"
	}
	g.ui = append(g.ui, m)
}

// handleUI serves the mount whose prefix best matches the request path.
func (g *Gateway) handleUI(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	m, ok := g.mountFor(r.URL.Path)
	if !ok || !g.serveFrom(w, r, m) {
		stdhttp.NotFound(w, r)
	}
}

// mountFor returns the longest-prefix mount matching path.
func (g *Gateway) mountFor(path string) (UIMount, bool) {
	var best UIMount
	found := false
	for _, m := range g.ui {
		prefix := strings.TrimSuffix(m.Prefix, "/")
		if prefix != "" && path != prefix && !strings.HasPrefix(path, prefix+"/") {
			continue
		}
		if !found || len(prefix) > len(strings.TrimSuffix(best.Prefix, "/")) {
			best, found = m, true
		}
	}
	return best, found
}

// serveFrom answers from one mount: the file itself, or index.html for a
// browser navigation to a client-side route. Returns false when the request is
// not something the UI can answer (a missing asset, an API call).
func (g *Gateway) serveFrom(w stdhttp.ResponseWriter, r *stdhttp.Request, m UIMount) bool {
	rel := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(m.Prefix, "/"))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "index.html"
	}
	if _, err := fs.Stat(m.FS, rel); err != nil {
		if !wantsHTML(r) {
			return false
		}
		rel = "index.html"
	}
	stdhttp.ServeFileFS(w, r, m.FS, rel)
	return true
}

// rootUI is the product's main interface, used as the shell for a project whose
// own WebUI is down.
func (g *Gateway) rootUI() (fs.FS, bool) {
	m, ok := g.mountFor("/")
	if !ok {
		return nil, false
	}
	return m.FS, true
}
