package control

import (
	"encoding/json"
	"fmt"
	"net"
	stdhttp "net/http"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"github.com/niq-run/niq/internal/project"
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
		views = append(views, projectView{Project: p, Running: c.isRunning(p.ID)})
	}
	json.NewEncoder(w).Encode(views)
}

// isRunning reports whether a project is actually up. A tracked subprocess is
// the authoritative signal for one this control plane started; otherwise we probe
// the project's persisted WebUI port, so a project started manually from the CLI
// (outside the control plane) is still shown as running.
func (c *Control) isRunning(id string) bool {
	// 1) Live process that this control plane launched?
	c.mu.Lock()
	proc, ok := c.procs[id]
	if ok && proc != nil {
		err := proc.Signal(syscall.Signal(0))
		if err != nil {
			delete(c.procs, id)
		} else {
			c.mu.Unlock()
			return true
		}
	}
	c.mu.Unlock()

	// 2) Fall back to probing the persisted WebUI port (catches manual starts).
	p, err := project.LoadProject(id)
	if err != nil || p.Ports.WebUI == 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI), 300*time.Millisecond)
	if err != nil {
		return false // nothing listening on the last-known WebUI port
	}
	conn.Close()
	return true
}

// handleRestartProject gracefully stops a running project (if one is tracked)
// and relaunches it on its reused ports. It waits for the old process to exit
// before spawning the new one so the two never contend for the persisted
// ports; launchProject then blocks until the new WebUI port is actually
// listening, so the response reflects a ready project.
func (c *Control) handleRestartProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if _, err := project.LoadProject(id); err != nil {
		stdhttp.Error(w, "project not found", 404)
		return
	}
	c.mu.Lock()
	proc, ok := c.procs[id]
	if ok && proc != nil {
		_ = proc.Signal(os.Interrupt) // graceful shutdown + snapshot
		c.mu.Unlock()
		exited := make(chan struct{})
		go func() { _, _ = proc.Wait(); close(exited) }()
		select {
		case <-exited:
		case <-time.After(8 * time.Second):
		}
		c.mu.Lock()
		if cur, ok := c.procs[id]; ok && cur == proc {
			delete(c.procs, id)
		}
		c.mu.Unlock()
	} else {
		c.mu.Unlock()
	}
	c.launchProject(w, id)
}

// handleStopProject terminates a running project's process and forgets it.
func (c *Control) handleStopProject(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	c.mu.Lock()
	proc, ok := c.procs[id]
	if ok && proc != nil {
		_ = proc.Signal(os.Interrupt) // graceful: the project snapshots on shutdown
		delete(c.procs, id)
	}
	c.mu.Unlock()
	if !ok {
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
	if _, err := project.CreateProject(body.ID, tmpl); err != nil {
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
	if c.isRunning(id) {
		p, err := project.LoadProject(id)
		var webui string
		if err == nil && p.Ports.WebUI != 0 {
			webui = fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"project":    id,
			"already":    true,
			"webui_url":  project.LocalhostURL(webui),
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
	// Track the process and drop it when it exits.
	c.mu.Lock()
	c.procs[id] = cmd.Process
	c.mu.Unlock()
	go func() {
		_ = cmd.Wait()
		c.mu.Lock()
		if cur, ok := c.procs[id]; ok && cur == cmd.Process {
			delete(c.procs, id)
		}
		c.mu.Unlock()
	}()

	// Wait until the new process is actually serving on its WebUI port, so the
	// response (and the UI's loading state) reflects a ready project rather
	// than one that merely assigned a port in project.json.
	webUI := waitWebUIReady(id)
	json.NewEncoder(w).Encode(map[string]any{
		"project":    id,
		"webui_url":  project.LocalhostURL(webUI),
		"webui_port": project.PortOf(webUI),
		"bus_port":   resolvedBus(id),
	})
}

// waitWebUIReady blocks until the project's WebUI port is actually accepting
// TCP connections (not merely assigned in project.json), or the deadline lapses.
// It reuses the persisted port, so it is safe to call right after a relaunch on
// the same address — it succeeds only once the new process is listening.
func waitWebUIReady(id string) string {
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if p, err := project.LoadProject(id); err == nil && p.Ports.WebUI != 0 {
			addr := "127.0.0.1:" + strconv.Itoa(p.Ports.WebUI)
			if conn, derr := net.DialTimeout("tcp", addr, 300*time.Millisecond); derr == nil {
				conn.Close()
				return addr
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

func resolvedBus(id string) int {
	if p, err := project.LoadProject(id); err == nil {
		return p.Ports.Bus
	}
	return 0
}
