// Background control-plane lifecycle: a bare `niq` detaches the control plane
// from the terminal, prints where it is reachable, and exits — so closing the
// terminal (or the shell sending SIGHUP) no longer takes the service down.
// `niq status` / `niq stop` / `niq restart` operate on the recorded process.
//
// The record lives at <niq root>/control.json: the child writes it as soon as
// its listen socket is bound, and the parent waits for it (and for the port to
// answer) before printing the banner, so both agree on the resolved address
// even when the user passed ":0".
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/niq-run/niq/app/control"
	"github.com/niq-run/niq/app/niqhome"
	"github.com/niq-run/niq/app/project"
)

// controlState is the on-disk record of a running control plane.
type controlState struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`    // address as requested; reused by restart
	Bound   string `json:"bound"`   // address actually bound; dialed for liveness
	Version string `json:"version"` // binary that started it
	Started string `json:"started"` // RFC3339
}

func controlStatePath() string { return filepath.Join(niqhome.Root(), "control.json") }

func controlLogPath() string { return filepath.Join(niqhome.Root(), "niq.log") }

func readControlState() (*controlState, bool) {
	b, err := os.ReadFile(controlStatePath())
	if err != nil {
		return nil, false
	}
	var st controlState
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 {
		return nil, false
	}
	return &st, true
}

// writeControlState records the running control plane. The write goes through
// a temp file and a rename so a reader (the starting parent, `niq status`)
// never sees a half-written record.
func writeControlState(st *controlState) {
	if err := os.MkdirAll(niqhome.Root(), 0755); err != nil {
		log.Printf("[control] record pid file: %v", err)
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		log.Printf("[control] record pid file: %v", err)
		return
	}
	path := controlStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0644); err != nil {
		log.Printf("[control] record pid file: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[control] record pid file: %v", err)
		_ = os.Remove(tmp)
	}
}

// clearControlState removes the record, but only if it still names pid — a
// losing instance (bind failed because another was already up) must not wipe
// the winner's record.
func clearControlState(pid int) {
	if st, ok := readControlState(); ok && st.PID != pid {
		return
	}
	_ = os.Remove(controlStatePath())
}

// probeAddr maps a listen address to something dialable from this machine:
// an unspecified host (":9527", "0.0.0.0:9527", "[::]:9527") means loopback.
func probeAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// isServing reports whether something accepts connections on addr.
func isServing(addr string) bool {
	pa := probeAddr(addr)
	if pa == "" {
		return false
	}
	conn, err := net.DialTimeout("tcp", pa, 300*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// isRunning reports whether the recorded control plane is both alive and
// answering. The pid check keeps a stale record — or a foreign process that
// grabbed the port after a crash — from reading as "running"; the dial check
// keeps a recycled pid from doing the same.
func isRunning(st *controlState) bool {
	return st != nil && processAlive(st.PID) && isServing(st.Bound)
}

// startBackground launches the control plane detached from this terminal.
func startBackground(addr, auth string) error {
	if st, ok := readControlState(); ok && isRunning(st) {
		return reportAlreadyRunning(st, addr)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	args := []string{"control", "--foreground", "--addr", addr}
	if auth != "" {
		args = append(args, "--auth", auth)
	}
	cmd := exec.Command(exe, args...)
	// nil streams: the child gets the null device, so nothing it writes keeps
	// this terminal (or its pipeline) alive. Its logs go to niq.log.
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start background process: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if st, ok := readControlState(); ok && st.PID == cmd.Process.Pid && isServing(st.Bound) {
			printControlStarted(st)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("control plane did not start within 20s; see %s:\n%s", controlLogPath(), logTail(10))
		}
		select {
		case err := <-exited:
			return fmt.Errorf("control plane exited during startup: %v; see %s:\n%s", err, controlLogPath(), logTail(10))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// reportAlreadyRunning answers a start request when a control plane is already
// up: same address is a no-op, a different one is a conflict the user must
// resolve (two planes would fight over the single exposed port anyway).
func reportAlreadyRunning(st *controlState, addr string) error {
	if project.PortOf(st.Bound) != project.PortOf(addr) {
		return fmt.Errorf("niq control is already running (pid %d) at %s; stop it with `niq stop` before starting on %s",
			st.PID, project.LocalhostURL(st.Bound), addr)
	}
	fmt.Printf("niq control is already running (pid %d, since %s)\n\n", st.PID, st.Started)
	printControlHints(st.Bound)
	return nil
}

// runControlForeground runs the control plane attached to this terminal (the
// mode the background starter re-executes itself into).
func runControlForeground(addr, auth string) error {
	if st, ok := readControlState(); ok && isRunning(st) {
		return fmt.Errorf("niq control is already running (pid %d) at %s; restart it with `niq restart`",
			st.PID, project.LocalhostURL(st.Bound))
	}
	setupLogging()
	err := control.RunControl(control.ControlOptions{
		Addr: addr,
		Auth: auth,
		OnReady: func(bound string) {
			writeControlState(&controlState{
				PID:     os.Getpid(),
				Addr:    addr,
				Bound:   bound,
				Version: version,
				Started: time.Now().Format(time.RFC3339),
			})
		},
	})
	clearControlState(os.Getpid())
	if err == nil {
		fmt.Println("niq control stopped")
	} else {
		// The background starter's only channel back to the user is this log
		// (its stderr is the null device), so a startup failure — a taken port,
		// most often — has to land here to be reportable.
		log.Printf("[control] %v", err)
	}
	return err
}

// stopControl stops the control plane and, when all is set, every project
// instance too. Projects are their own processes: they outlive the plane that
// proxied them, so a plain stop leaves them serving on their loopback ports.
func stopControl(all bool) error {
	if err := stopControlPlane(); err != nil {
		return err
	}
	if all {
		return stopProjects()
	}
	reportRunningProjects()
	return nil
}

// stopControlPlane stops the recorded control plane, if there is one.
func stopControlPlane() error {
	st, ok := readControlState()
	if !ok {
		fmt.Println("niq control is not running")
		return nil
	}
	if !isRunning(st) {
		clearControlState(st.PID)
		fmt.Println("niq control is not running (cleared a stale record)")
		return nil
	}
	if err := terminateProcess(st.PID); err != nil {
		return fmt.Errorf("stop pid %d: %w", st.PID, err)
	}
	// The shutdown closes the listener last, so the process being gone means
	// the ports are free for an immediate restart.
	deadline := time.Now().Add(10 * time.Second)
	for processAlive(st.PID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("control plane (pid %d) did not exit within 10s", st.PID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	clearControlState(st.PID)
	fmt.Printf("niq control stopped (pid %d)\n", st.PID)
	return nil
}

// runningProjectIDs lists the projects whose persisted WebUI port answers,
// i.e. the instances a plain `niq stop` leaves alone.
func runningProjectIDs() []string {
	projects, err := project.ListProjects()
	if err != nil {
		return nil
	}
	var ids []string
	for _, p := range projects {
		if projectRunning(p) {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func projectRunning(p project.Project) bool {
	return p.Ports.WebUI != 0 && isServing(fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI))
}

// reportRunningProjects keeps a plain `niq stop` honest: the plane is down but
// these instances still hold their ports, and an agent session may still be
// running in them.
func reportRunningProjects() {
	ids := runningProjectIDs()
	if len(ids) == 0 {
		return
	}
	fmt.Printf("\n%d project instance(s) still running (%s); `niq stop --all` stops them too.\n",
		len(ids), strings.Join(ids, ", "))
}

// stopProjects interrupts every running project instance. SIGINT is the same
// graceful path Ctrl+C takes — 'niq project run' snapshots its state — and it
// reaches both instances this control plane launched and ones started by hand
// in another terminal (they are found by their WebUI port).
func stopProjects() error {
	projects, err := project.ListProjects()
	if err != nil {
		return err
	}
	var running []project.Project
	for _, p := range projects {
		if projectRunning(p) {
			running = append(running, p)
		}
	}
	if len(running) == 0 {
		fmt.Println("no project instances running")
		return nil
	}
	var ids []string
	for _, p := range running {
		if project.InterruptPortHolders(p.Ports.WebUI) > 0 {
			ids = append(ids, p.ID)
		}
	}
	// Shutdown is asynchronous: wait for the ports to close, so "stopped" is
	// true by the time the command returns.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var left []string
		for _, p := range running {
			if projectRunning(p) {
				left = append(left, p.ID)
			}
		}
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("project instance(s) %s did not exit within 10s", strings.Join(left, ", "))
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Printf("stopped %d project instance(s): %s\n", len(ids), strings.Join(ids, ", "))
	return nil
}

func restartControl() error {
	addr := ":9527"
	if st, ok := readControlState(); ok {
		addr = st.Addr // keep the port the user originally chose
		if isRunning(st) {
			if err := stopControl(false); err != nil {
				return err
			}
		} else {
			clearControlState(st.PID)
		}
	}
	// Credentials survive in ~/.niq/common/auth.json, so a restart needs no
	// --auth to keep remote access behind the same basic-auth pair.
	return startBackground(addr, "")
}

func statusControl() error {
	st, ok := readControlState()
	if !ok || !isRunning(st) {
		fmt.Println("niq control is not running")
		fmt.Println()
		fmt.Println("Start it with `niq` (background) or `niq control --foreground`.")
		printProjectStatus("")
		return nil
	}
	fmt.Printf("niq control is running (pid %d, since %s)\n\n", st.PID, st.Started)
	printControlHints(st.Bound)
	printProjectStatus(project.LocalhostURL(st.Bound))
	if st.Version != version {
		fmt.Printf("\nStarted by niq %s; this binary is %s — run `niq restart` to pick it up.\n", st.Version, version)
	}
	return nil
}

// printProjectStatus lists every project with the state of its instance, so
// `niq status` answers "what is actually up right now?" without opening the
// WebUI — the dev loop's usual question when a project page stops responding.
// A project's ports are persisted, so a refused dial means that instance is not
// running (stopped, or died and was never restarted): niq therefore reports it
// as stopped even though the control plane still proxies its old port.
//
// controlURL, when the control plane is up, is the address that reaches the
// project from outside; otherwise the loopback URL is shown.
func printProjectStatus(controlURL string) {
	projects, err := project.ListProjects()
	if err != nil {
		return
	}
	if len(projects) == 0 {
		return
	}
	fmt.Println("\nprojects")
	for _, p := range projects {
		state, url := "stopped", "-"
		if projectRunning(p) {
			state = "running"
			url = project.LocalhostURL(fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI))
			if controlURL != "" {
				url = strings.TrimSuffix(controlURL, "/") + "/p/" + p.ID + "/"
			}
		}
		fmt.Printf("  %-14s %-8s %s\n", p.ID, state, url)
	}
}

func printControlStarted(st *controlState) {
	fmt.Printf("niq control is running in the background (pid %d)\n\n", st.PID)
	printControlHints(st.Bound)
	fmt.Printf("\nRun it attached to the terminal with `niq control --foreground`.\n")
}

func printControlHints(bound string) {
	fmt.Printf("  WebUI    %s\n", project.LocalhostURL(bound))
	fmt.Printf("  Log      %s\n", controlLogPath())
	fmt.Printf("  Stop     niq stop\n")
	fmt.Printf("  Restart  niq restart\n")
}

// logTail returns the last n lines of the control-plane log, so a failed start
// can show the actual reason (typically "address already in use") instead of
// just "exited".
func logTail(n int) string {
	b, err := os.ReadFile(controlLogPath())
	if err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
