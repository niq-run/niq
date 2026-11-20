package webui

import (
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/niq-run/niq/app/standalone/launcher"
	"github.com/niq-run/niq/app/standalone/project/template"
	"github.com/niq-run/niq/app/standalone/store"
)

// projectView augments a project definition with its live run state.
type projectView = launcher.View

// handleListProjects returns the project definitions plus live run state.
func (c *Shell) handleListProjects(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	views, err := launcher.List()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	json.NewEncoder(w).Encode(views)
}

// dirItem is one subdirectory the server-side directory picker offers.
type dirItem struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// handleListDirs returns the immediate subdirectories of an absolute path (the
// webui's server-side directory picker). A browser file picker cannot expose a
// server-side absolute path even on localhost, so the server browses the
// filesystem and the UI navigates folders instead — this works locally and
// remotely alike. It only lists directory names, never file contents. An empty
// path starts at the niq projects root.
func (c *Shell) handleListDirs(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	root := r.URL.Query().Get("path")
	if root == "" {
		root = store.ProjectsRoot()
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		stdhttp.Error(w, "not a directory: "+abs, 400)
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	dirs := make([]dirItem, 0, len(entries))
	for _, de := range entries {
		// Resolve symlinks so the picker can descend into linked folders.
		path := filepath.Join(abs, de.Name())
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		dirs = append(dirs, dirItem{Name: de.Name(), Path: path})
	}
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
	json.NewEncoder(w).Encode(dirs)
}

// handleLinkProject opens an external project directory by symlinking it into
// the niq root (see launcher.LinkProject). The webui posts the directory the
// user chose; it may already be a niq project and resume as-is.
func (c *Shell) handleLinkProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		stdhttp.Error(w, "path is required", 400)
		return
	}
	p, err := launcher.LinkProject(body.Path)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	json.NewEncoder(w).Encode(projectView{Project: p, Running: launcher.IsRunning(p)})
}

// handleRestartProject stops the running instance and relaunches it on its
// reused ports. The instance is found through the port it holds, so one the
// shell spawned and one started elsewhere take exactly this path.
func (c *Shell) handleRestartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := launcher.LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	inst, err := launcher.Restart(id)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadGateway)
		return
	}
	json.NewEncoder(w).Encode(inst)
}

// handleStopProject terminates a running project by interrupting whoever
// listens on its persisted WebUI port.
func (c *Shell) handleStopProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := launcher.LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	err := launcher.Stop(id)
	switch {
	case err == nil:
		w.WriteHeader(stdhttp.StatusAccepted)
	case errors.Is(err, launcher.ErrNotRunning):
		stdhttp.Error(w, "project not running", 404)
	default:
		stdhttp.Error(w, err.Error(), 500)
	}
}

// handleCreateProject creates a project from a named template and immediately
// starts it, returning the resolved instance.
func (c *Shell) handleCreateProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		ID       string `json:"id"`
		Template string `json:"template"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		stdhttp.Error(w, "id is required", 400)
		return
	}
	if body.Template == "" {
		body.Template = "default"
	}
	tmpl, err := template.Load(template.TemplatesDir(), body.Template)
	if err != nil {
		stdhttp.Error(w, "unknown template: "+body.Template, 400)
		return
	}
	if _, err := launcher.CreateProject(body.ID, body.Template, tmpl); err != nil {
		stdhttp.Error(w, err.Error(), 409)
		return
	}
	c.respondStart(w, body.ID)
}

// handleStartProject launches a project as its own process on its persisted
// ports and returns the resolved instance.
func (c *Shell) handleStartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := launcher.LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	c.respondStart(w, id)
}

func (c *Shell) respondStart(w stdhttp.ResponseWriter, id string) {
	inst, err := launcher.Start(id)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadGateway)
		return
	}
	json.NewEncoder(w).Encode(inst)
}
