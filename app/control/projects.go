package control

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	stdhttp "net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/niq-run/niq/app/project"
)

// projectView augments a project definition with its live run state.
type projectView struct {
	project.Project
	Running bool `json:"running"`
}

// handleListProjects returns the project definitions plus live run state.
func (c *Control) handleListProjects(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	projects, err := project.ListProjects()
	if err != nil {
		stdhttp.Error(w, err.Error(), 500)
		return
	}
	views := make([]projectView, 0, len(projects))
	for _, p := range projects {
		views = append(views, projectView{Project: p, Running: isRunning(p.ID)})
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
func (c *Control) handleListDirs(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	root := r.URL.Query().Get("path")
	if root == "" {
		root = project.ProjectsRoot()
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
// the niq root (see project.LinkProject). The webui posts the directory the
// user chose; it may already be a niq project and resume as-is.
func (c *Control) handleLinkProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Path == "" {
		stdhttp.Error(w, "path is required", 400)
		return
	}
	p, err := project.LinkProject(body.Path)
	if err != nil {
		stdhttp.Error(w, err.Error(), 400)
		return
	}
	json.NewEncoder(w).Encode(projectView{Project: *p, Running: isRunning(p.ID)})
}

// isRunning reports whether a project is actually up: its persisted WebUI port
// answers, whoever started the process. The control plane keeps no list of the
// children it spawned — a project it launched, one started by hand in a
// terminal, and one that outlived a previous control plane are the same thing
// here, and are managed the same way.
func isRunning(id string) bool {
	p, err := project.LoadProject(id)
	if err != nil || p.Ports.WebUI == 0 {
		return false
	}
	return probePort(p.Ports.WebUI)
}

// probePort reports whether something accepts connections on the project's
// loopback WebUI port.
func probePort(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// handleRestartProject stops the running instance and relaunches it on its
// reused ports. The instance is found through the port it holds — the control
// plane does not track pids, so one it spawned and one started elsewhere take
// exactly this path — and the launch waits for that port to be released, so the
// two never contend for the persisted address.
func (c *Control) handleRestartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	p, err := project.LoadProject(id)
	if err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	project.InterruptPortHolders(p.Ports.WebUI)
	waitPortFree(p.Ports.WebUI)
	c.launchProject(w, id)
}

// waitPortFree blocks until nothing listens on port (or the deadline lapses),
// so the relaunch binds the persisted port instead of falling back to a random
// one — the visible "port jumping".
func waitPortFree(port int) {
	if port == 0 {
		return
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(100 * time.Millisecond)
	}
}

// handleStopProject terminates a running project by interrupting whoever
// listens on its persisted WebUI port — the same port-holder idea as the
// project's own startup port reclaim, and the reason a project started by hand
// stops exactly like one this control plane spawned.
func (c *Control) handleStopProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	p, err := project.LoadProject(id)
	if err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	if project.InterruptPortHolders(p.Ports.WebUI) == 0 {
		stdhttp.Error(w, "project not running", 404)
		return
	}
	w.WriteHeader(stdhttp.StatusAccepted)
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
		body.Template = "default"
	}
	tmpl, err := project.LoadTemplate(project.TemplatesDir(), body.Template)
	if err != nil {
		stdhttp.Error(w, "unknown template: "+body.Template, 400)
		return
	}
	if _, err := project.CreateProject(body.ID, body.Template, tmpl); err != nil {
		stdhttp.Error(w, err.Error(), 409)
		return
	}
	c.launchProject(w, body.ID)
}

// handleStartProject launches a project as its own process on dynamic ports and
// returns the resolved WebUI URL for the user to be redirected to.
func (c *Control) handleStartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := project.LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	c.launchProject(w, id)
}

// launchProject spawns `niq project run <id>` on its (re-used) ports and answers
// with the project's resolved WebUI URL. If the project is already running it
// does NOT spawn a duplicate — that would collide on the persisted ports and
// force the fallback to a fresh random port each start (the visible "jumping").
func (c *Control) launchProject(w stdhttp.ResponseWriter, id string) {
	if isRunning(id) {
		p, err := project.LoadProject(id)
		var webui string
		if err == nil && p.Ports.WebUI != 0 {
			webui = fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"project":    id,
			"already":    true,
			"webui_port": project.PortOf(webui),
		})
		return
	}
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
	// Reap the child and keep its exit result: a project killed by an eventbus
	// panic exits with code 2 (Go's panic) and no shutdown seq, and discarding
	// cmd.Wait() hid exactly that in the field. The channel is buffered so the
	// reaper never blocks once startup has been answered.
	exited := make(chan error, 1)
	go func() {
		werr := cmd.Wait()
		if werr != nil {
			log.Printf("[control] project %s exited abnormally: %v", id, werr)
		} else {
			log.Printf("[control] project %s exited cleanly", id)
		}
		exited <- werr
	}()

	// Wait until the new process is actually serving on its WebUI port, so the
	// response (and the UI's loading state) reflects a ready project rather
	// than one that merely assigned a port in project.json. A child that dies
	// during startup is answered as a failure with its log — the old path
	// returned 200 with webui_port 0, which the UI could not report at all.
	webUI, err := waitProjectStart(id, exited)
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadGateway)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"project":    id,
		"webui_port": project.PortOf(webUI),
		"bus_port":   resolvedBus(id),
	})
}

// projectStartTimeout bounds how long a start waits for the new instance's WebUI
// to accept connections. Matches the CLI picker's own wait.
const projectStartTimeout = 20 * time.Second

// waitProjectStart blocks until the project's WebUI port is actually accepting
// TCP connections (not merely assigned in project.json), its process exits, or
// the deadline lapses. The exited channel carries the child's Wait result; it
// is buffered, so an exit long after a successful start is still reaped and
// logged by the caller's goroutine.
func waitProjectStart(id string, exited <-chan error) (string, error) {
	deadline := time.Now().Add(projectStartTimeout)
	for {
		if p, err := project.LoadProject(id); err == nil && p.Ports.WebUI != 0 {
			addr := "127.0.0.1:" + strconv.Itoa(p.Ports.WebUI)
			if conn, derr := net.DialTimeout("tcp", addr, 300*time.Millisecond); derr == nil {
				conn.Close()
				return addr, nil
			}
		}
		select {
		case werr := <-exited:
			return "", fmt.Errorf("project %s exited during startup: %v%s", id, werr, logHint(id))
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("project %s did not start within %s%s", id, projectStartTimeout, logHint(id))
		}
	}
}

// logHint points at the project's log and appends its tail, so the reason a
// project refused to start (no usable provider, a port already in use) reaches
// the UI instead of sitting in a file the user has to go find.
func logHint(id string) string {
	path := project.LogPath(id)
	hint := "; see " + path
	if tail := logTail(path, 4); tail != "" {
		return hint + ":\n" + tail
	}
	return hint
}

// logTail returns the last n non-empty trailing lines of a file, or "" when it
// cannot be read. Only the file's tail is loaded: project logs grow by the day.
func logTail(path string, n int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	const window = 4096
	off := st.Size() - window
	if off < 0 {
		off = 0
	}
	buf := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(buf, off); err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func resolvedBus(id string) int {
	if p, err := project.LoadProject(id); err == nil {
		return p.Ports.Bus
	}
	return 0
}
