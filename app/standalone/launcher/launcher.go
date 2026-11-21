// Package launcher owns project instances: it lists them, starts one as its
// own process, stops it, and restarts it. It is the only package that knows how
// a project process is invoked and how an instance is identified (by its
// persisted WebUI port, not by a parent/child table).
//
// The launcher is not a daemon. A project started here, one started by hand,
// and one that outlived a previous shell are the same thing: an address that
// either answers or does not.
package launcher

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/niq-run/niq/app/standalone/contract"
	"github.com/niq-run/niq/app/standalone/helper"
	"github.com/niq-run/niq/app/standalone/project"
	"github.com/niq-run/niq/app/standalone/store"
)

// ChildVerb is the private argv word that turns this binary into a project
// runtime. It is not a user command: main dispatches it before the CLI, and
// only the launcher emits it.
const ChildVerb = "__project-run"

// ErrNotRunning is returned by Stop when nothing answers on the project port.
var ErrNotRunning = fmt.Errorf("project is not running")

// List returns every project with its running state.
func List() ([]contract.View, error) {
	projects, err := project.ListProjects()
	if err != nil {
		return nil, err
	}
	views := make([]contract.View, 0, len(projects))
	for _, p := range projects {
		views = append(views, ViewOf(p))
	}
	return views, nil
}

// ViewOf is the one place that turns a stored project into what a client sees.
// The wire shape is a decision made here, not an inheritance from project.json:
// paths are resolved on this side so a client never learns the layout, and the
// run state is probed now rather than stored.
func ViewOf(p store.Project) contract.View {
	return contract.View{
		ID:        p.ID,
		CreatedAt: p.CreatedAt,
		Ports:     p.Ports,
		Dir:       store.ProjectDir(p.ID),
		Running:   IsRunning(p),
	}
}

// IsRunning reports whether the project's persisted WebUI port answers.
func IsRunning(p store.Project) bool { return IsRunningPort(p.Ports.WebUI) }

// IsRunningPort reports whether something answers on the instance's WebUI port.
// An instance is identified by that port, so this is the whole of "is it up" —
// which is also why a caller holding only a View can ask.
func IsRunningPort(port int) bool { return port != 0 && helper.IsServingPort(port) }

// Start brings a project up on its persisted ports and waits until its WebUI
// answers. Starting an already-running project is not an error: the same
// instance is returned with Already set.
func Start(id string) (contract.Instance, error) {
	p, err := project.LoadProject(id)
	if err != nil {
		return contract.Instance{}, err
	}
	if IsRunning(*p) {
		return instanceOf(*p, true), nil
	}
	exe, err := os.Executable()
	if err != nil {
		return contract.Instance{}, fmt.Errorf("launcher: resolve executable: %w", err)
	}
	// No --bus/--webui: reuse the persisted ports so a restart lands on the
	// same addresses instead of jumping.
	cmd := exec.Command(exe, ChildVerb, id)
	cmd.SysProcAttr = projectProcAttr()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return contract.Instance{}, fmt.Errorf("launcher: start %s: %w", id, err)
	}
	// Keep the exit result: a project killed by a panic exits with code 2 and
	// no shutdown sequence, and discarding Wait hid exactly that in the field.
	exited := make(chan error, 1)
	go func() {
		werr := cmd.Wait()
		if werr != nil {
			log.Printf("[launcher] project %s exited abnormally: %v", id, werr)
		} else {
			log.Printf("[launcher] project %s exited cleanly", id)
		}
		exited <- werr
	}()

	if _, err := waitStart(id, exited); err != nil {
		return contract.Instance{}, err
	}
	p, err = project.LoadProject(id)
	if err != nil {
		return contract.Instance{}, err
	}
	return contract.Instance{ID: id, WebUIPort: p.Ports.WebUI, BusPort: p.Ports.Bus}, nil
}

// Stop interrupts whoever listens on the project's WebUI port. It returns
// ErrNotRunning when nothing does.
func Stop(id string) error {
	p, err := project.LoadProject(id)
	if err != nil {
		return err
	}
	if project.InterruptPortHolders(p.Ports.WebUI) == 0 {
		return ErrNotRunning
	}
	return nil
}

// Restart stops a running instance and starts a new one on the same ports.
func Restart(id string) (contract.Instance, error) {
	p, err := project.LoadProject(id)
	if err != nil {
		return contract.Instance{}, err
	}
	if IsRunning(*p) {
		project.InterruptPortHolders(p.Ports.WebUI)
		waitPortFree(p.Ports.WebUI)
	}
	return Start(id)
}

// WaitStopped blocks until the project's WebUI port stops answering, so a
// caller that reports "stopped" is telling the truth.
func WaitStopped(id string, timeout time.Duration) error {
	p, err := project.LoadProject(id)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for IsRunning(*p) {
		if time.Now().After(deadline) {
			return fmt.Errorf("project %s is still listening on port %d after %s", id, p.Ports.WebUI, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

// LogHint points at the project's log and appends its tail, so a startup
// failure reaches the caller instead of sitting in a file.
func LogHint(id string) string {
	path := store.LogPath(id)
	hint := "; see " + path
	if tail := logTail(path, 4); tail != "" {
		return hint + ":\n" + tail
	}
	return hint
}

// IsChild reports whether argv is the launcher's private runtime invocation.
func IsChild(argv []string) bool {
	return len(argv) > 1 && argv[1] == ChildVerb
}

// ChildMain runs the project runtime in this process. main calls it before the
// CLI, so the CLI never learns this verb exists.
func ChildMain(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("launcher: %s takes exactly one project id", ChildVerb)
	}
	project.SetupLogging(args[0])
	return project.RunProject(project.ProjectRunOptions{ProjectID: args[0]})
}

func instanceOf(p store.Project, already bool) contract.Instance {
	return contract.Instance{ID: p.ID, WebUIPort: p.Ports.WebUI, BusPort: p.Ports.Bus, Already: already}
}

// startTimeout bounds how long Start waits for the new WebUI to accept.
const startTimeout = 20 * time.Second

func waitStart(id string, exited <-chan error) (string, error) {
	deadline := time.Now().Add(startTimeout)
	for {
		if p, err := project.LoadProject(id); err == nil && p.Ports.WebUI != 0 {
			addr := "127.0.0.1:" + strconv.Itoa(p.Ports.WebUI)
			if helper.IsServing(addr) {
				return addr, nil
			}
		}
		select {
		case werr := <-exited:
			return "", fmt.Errorf("project %s exited during startup: %v%s", id, werr, LogHint(id))
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("project %s did not start within %s%s", id, startTimeout, LogHint(id))
		}
	}
}

func waitPortFree(port int) {
	if port == 0 {
		return
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !helper.IsServingPort(port) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// logTail returns the last n non-empty trailing lines of a file.
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
