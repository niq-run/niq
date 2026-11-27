// Background control-plane lifecycle: a bare `niq` detaches the control plane
// from the terminal, prints where it is reachable, and exits — so closing the
// terminal (or the shell sending SIGHUP) no longer takes the service down.
// `niq status` / `niq stop` / `niq restart` operate on the recorded process.
//
// The record lives at <niq root>/shell.json: the child writes it as soon as
// its listen socket is bound, and the parent waits for it (and for the port to
// answer) before printing the banner, so both agree on the resolved address
// even when the user passed ":0".
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/niq-run/niq/app/client/tui/picker"
	"github.com/niq-run/niq/app/standalone/contract"
	"github.com/niq-run/niq/app/standalone/gateway"
	"github.com/niq-run/niq/app/standalone/helper"
	"github.com/niq-run/niq/app/standalone/launcher"
	"github.com/niq-run/niq/app/standalone/store"
)

// shellState is the on-disk record of a running control plane.
type shellState struct {
	PID     int    `json:"pid"`
	Addr    string `json:"addr"`    // address as requested; reused by restart
	Bound   string `json:"bound"`   // address actually bound; dialed for liveness
	Started string `json:"started"` // RFC3339
}

// defaultAddr is where the control plane listens unless --addr says otherwise.
// Loopback by default: exposing the plane to other machines is an explicit
// choice that also requires basic auth.
const defaultAddr = "127.0.0.1:9527"

func shellStatePath() string { return filepath.Join(store.Root(), "shell.json") }

// setupLogging sends the shell daemon's output to <root>/niq.log.
func setupLogging() {
	path := shellLogPath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.SetOutput(io.Discard)
		return
	}
	log.SetOutput(f)
}

func shellLogPath() string { return filepath.Join(store.Root(), "niq.log") }

func readShellState() (*shellState, bool) {
	b, err := os.ReadFile(shellStatePath())
	if err != nil {
		return nil, false
	}
	var st shellState
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 {
		return nil, false
	}
	return &st, true
}

// writeShellState records the running control plane. The write goes through
// a temp file and a rename so a reader (the starting parent, `niq status`)
// never sees a half-written record.
func writeShellState(st *shellState) {
	if err := os.MkdirAll(store.Root(), 0755); err != nil {
		log.Printf("[shell] record pid file: %v", err)
		return
	}
	b, err := json.Marshal(st)
	if err != nil {
		log.Printf("[shell] record pid file: %v", err)
		return
	}
	path := shellStatePath()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0644); err != nil {
		log.Printf("[shell] record pid file: %v", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[shell] record pid file: %v", err)
		_ = os.Remove(tmp)
	}
}

// clearShellState removes the record, but only if it still names pid — a
// losing instance (bind failed because another was already up) must not wipe
// the winner's record.
func clearShellState(pid int) {
	if st, ok := readShellState(); ok && st.PID != pid {
		return
	}
	_ = os.Remove(shellStatePath())
}

// isRunning reports whether the recorded control plane is both alive and
// answering. The pid check keeps a stale record — or a foreign process that
// grabbed the port after a crash — from reading as "running"; the dial check
// keeps a recycled pid from doing the same.
func isRunning(st *shellState) bool {
	return st != nil && helper.Alive(st.PID) && helper.IsServing(st.Bound)
}

// gatewayRunning is isRunning for the recorded state on disk: the front-end's
// "is there anything to decide?" check.
func gatewayRunning() bool {
	st, ok := readShellState()
	return ok && isRunning(st)
}

// startBackground launches the control plane detached from this terminal.
// startBackground starts the detached control plane and returns once it is
// serving. Printing is the caller's job: a start reports the whole service, a
// restart reports only the plane (its project table would be about to change).
func startBackground(addr, auth string) (*shellState, error) {
	if st, ok := readShellState(); ok && isRunning(st) {
		return nil, reportAlreadyRunning(st, addr)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	args := []string{"w", "--foreground", "--addr", addr}
	if auth != "" {
		args = append(args, "--auth", auth)
	}
	cmd := exec.Command(exe, args...)
	// nil streams: the child gets the null device, so nothing it writes keeps
	// this terminal (or its pipeline) alive. Its logs go to niq.log.
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start background process: %w", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if st, ok := readShellState(); ok && st.PID == cmd.Process.Pid && helper.IsServing(st.Bound) {
			return st, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the gateway did not start within 20s; see %s:\n%s", shellLogPath(), logTail(10))
		}
		select {
		case err := <-exited:
			return nil, fmt.Errorf("the gateway exited during startup: %v; see %s:\n%s", err, shellLogPath(), logTail(10))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// reportAlreadyRunning answers a start request when a control plane is already
// up: same address is a no-op that reports instead, a different one is a
// conflict the user must resolve (two planes would fight over the single
// exposed port anyway).
func reportAlreadyRunning(st *shellState, addr string) error {
	if store.PortOf(st.Bound) != store.PortOf(addr) {
		return fmt.Errorf("niq gateway is already running (pid %d) at %s; stop it with `niq stop` before starting on %s",
			st.PID, brand(store.LocalhostURL(st.Bound)), addr)
	}
	return printStatus()
}

// runShellForeground runs the control plane attached to this terminal (the
// mode the background starter re-executes itself into).
func runShellForeground(addr, auth string) error {
	if st, ok := readShellState(); ok && isRunning(st) {
		return fmt.Errorf("niq gateway is already running (pid %d) at %s; restart it with `niq restart`",
			st.PID, store.LocalhostURL(st.Bound))
	}
	setupLogging()
	err := gateway.Run(gateway.Options{
		Addr: addr,
		Auth: auth,
		UI:   ui,
		OnReady: func(bound string) {
			writeShellState(&shellState{
				PID:     os.Getpid(),
				Addr:    addr,
				Bound:   bound,
				Started: time.Now().Format(time.RFC3339),
			})
		},
	})
	clearShellState(os.Getpid())
	if err == nil {
		fmt.Printf("%s %s\n", bold("niq gateway"), green("stopped"))
	} else {
		// The background starter's only channel back to the user is this log
		// (its stderr is the null device), so a startup failure — a taken port,
		// most often — has to land here to be reportable.
		log.Printf("[shell] %v", err)
	}
	return err
}

// stopShell stops the control plane and, when all is set, every project
// instance too. Projects are their own processes: they outlive the plane that
// proxied them, so a plain stop leaves them serving on their loopback ports.
func stopShell(all bool) error {
	if err := stopShellProcess(); err != nil {
		return err
	}
	if all {
		return stopProjects()
	}
	reportRunningProjects()
	return nil
}

// stopShellProcess stops the recorded control plane, if there is one.
func stopShellProcess() error {
	st, ok := readShellState()
	if !ok {
		fmt.Printf("%s %s\n", bold("niq gateway"), dim("not running"))
		return nil
	}
	if !isRunning(st) {
		clearShellState(st.PID)
		fmt.Printf("%s %s\n", bold("niq gateway"), dim("not running (cleared a stale record)"))
		return nil
	}
	if err := terminateProcess(st.PID); err != nil {
		return fmt.Errorf("stop pid %d: %w", st.PID, err)
	}
	// The shutdown closes the listener last, so the process being gone means
	// the ports are free for an immediate restart.
	deadline := time.Now().Add(10 * time.Second)
	for helper.Alive(st.PID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("control plane (pid %d) did not exit within 10s", st.PID)
		}
		time.Sleep(100 * time.Millisecond)
	}
	clearShellState(st.PID)
	fmt.Printf("%s %s %s\n", bold("niq gateway"), green("stopped"), dim(fmt.Sprintf("pid %d", st.PID)))
	return nil
}

// runningProjectIDs lists the projects whose persisted WebUI port answers,
// i.e. the instances a plain `niq stop` leaves alone.
func runningProjectIDs() []string {
	views, err := launcher.List()
	if err != nil {
		return nil
	}
	var ids []string
	for _, v := range views {
		if v.Running {
			ids = append(ids, v.ID)
		}
	}
	return ids
}

// reportRunningProjects keeps a plain `niq stop` honest: the plane is down but
// these instances still hold their ports, and an agent session may still be
// running in them.
func reportRunningProjects() {
	ids := runningProjectIDs()
	if len(ids) == 0 {
		return
	}
	fmt.Printf("\n%s %d project instance(s) still running (%s); %s stops them too.\n",
		dim("note:"), len(ids), strings.Join(ids, ", "), bold("niq stop -p"))
}

// stopProjects interrupts every running project instance. SIGINT is the same
// graceful path Ctrl+C takes — the project snapshots its state on shutdown —
// and it reaches both instances this control plane launched and ones started by
// hand in another terminal (they are found by their WebUI port).
func stopProjects() error {
	// The project package logs the signal it sends to a port holder; on a
	// report like this it would land between the control line and the summary.
	prev := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prev)

	views, err := launcher.List()
	if err != nil {
		return err
	}
	var running []contract.View
	for _, v := range views {
		if v.Running {
			running = append(running, v)
		}
	}
	if len(running) == 0 {
		fmt.Println("no project instances running")
		return nil
	}
	var ids []string
	for _, p := range running {
		if err := launcher.Stop(p.ID); err == nil {
			ids = append(ids, p.ID)
		}
	}
	// Shutdown is asynchronous: wait for the ports to close, so "stopped" is
	// true by the time the command returns.
	deadline := time.Now().Add(10 * time.Second)
	for {
		var left []string
		for _, p := range running {
			if launcher.IsRunningPort(p.Ports.WebUI) {
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
	fmt.Printf("%s %d project instance(s): %s\n", green("stopped"), len(ids), strings.Join(ids, ", "))
	return nil
}

// restartControl stops and starts the control plane again. With withProjects it
// also restarts the instances that were running, which is what a binary upgrade
// needs: every process serves the page it was built with, so leaving the old
// ones up would leave the old UI up.
//
// The set is captured before the plane goes down. Instances are independent of
// it, so nothing they do depends on the order — but capturing first makes clear
// that "running now" is the whole criterion, whether the plane started them or
// a terminal did.
func restartControl(withProjects bool) error {
	var running []string
	if withProjects {
		running = runningProjectIDs()
	}

	addr := defaultAddr
	if st, ok := readShellState(); ok {
		addr = st.Addr // keep the port the user originally chose
		if isRunning(st) {
			// Not stopShell: it reports the projects it left running, which
			// this command is about to restart.
			if err := stopShellProcess(); err != nil {
				return err
			}
		} else {
			clearShellState(st.PID)
		}
	} else {
		// Nothing recorded (after `niq stop`, or a first run): honour the
		// remembered preference, but never ask — restart means "bring back
		// what was there", not "decide how to expose it".
		if saved, err := resolveStartAddr(startRequest{}, nil, authConfigured()); err == nil {
			addr = saved
		}
	}
	// Credentials survive in ~/.niq/common/auth.json, so a restart needs no
	// --auth to keep remote access behind the same basic-auth pair.
	st, err := ensureGateway(addr, "", askOnTerminal())
	if err != nil {
		return err
	}
	if st != nil {
		// The plane's own report only: the project table would show states that
		// the next step changes. A plain restart keeps the full banner.
		if withProjects {
			printControlRunning(st)
		} else {
			printShellStarted(st)
		}
	}
	if !withProjects {
		return nil
	}

	if len(running) == 0 {
		fmt.Printf("\n%s\n", dim("no project instance was running"))
		return nil
	}
	fmt.Printf("\n%s\n", bracketed("projects"))
	restartProjectInstances(running)
	return nil
}

// restartProjectInstances restarts each id on its recorded ports and prints one
// line per project. A project that fails is reported and the rest continue: one
// instance failing to come back should not strand the others half-upgraded.
func restartProjectInstances(ids []string) {
	// The project package logs the signal it sends to a port holder on the
	// default logger; on a report like this those lines would land in the
	// middle of the table, and the per-project line below already says what
	// happened.
	prev := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prev)

	for _, id := range ids {
		inst, err := launcher.Restart(id)
		if err != nil {
			fmt.Printf("%s %s\n", bold(id), red(err.Error()))
			continue
		}
		fmt.Printf("%s %s %s\n", bold(id), green("restarted"),
			brand(fmt.Sprintf("http://localhost:%d", inst.WebUIPort)))
	}
}

func printStatus() error {
	st, ok := readShellState()
	if !ok || !isRunning(st) {
		printControlStopped()
		printProjectStatus("")
		return nil
	}
	printControlRunning(st)
	printProjectStatus(store.LocalhostURL(st.Bound))
	return nil
}

// printControlRunning is the control-plane half of `niq status`: identity,
// state, address and the commands that act on it. Started/status/already-running
// all print it, so the service always looks the same no matter which command
// you used to ask.
func printControlRunning(st *shellState) {
	fmt.Print(controlHeader(st))
}

// controlHeader renders the running control plane's identity block — the banner,
// the gateway state, and the webui/remote/log/stop/restart lines — as text. The
// picker leads with it so `niq i` shows the same basic information as a bare
// `niq`, rather than only the project list.
func controlHeader(st *shellState) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("%s\n", versionTag())

	w("\n%s\n", bracketed("gateway")+" "+stateWord(true)+
		dim(fmt.Sprintf("  pid %d · since %s", st.PID, shortTime(st.Started))))
	w("%s %s  %s\n", label("webui", 8), brand(store.LocalhostURL(st.Bound)), dim(exposure(st.Bound)))
	// Say what remote access looks like from here: either the address another
	// machine would open, or why there is none yet.
	if u := remoteURL(st.Bound); u != "" {
		w("%s %s\n", label("remote", 8), brand(u))
	} else if helper.IsLoopbackBind(st.Bound) && !authConfigured() {
		w("%s %s\n", label("remote", 8), dim("off — needs auth first: niq --auth user:pass"))
	}
	w("%s %s\n", label("log", 8), shortPath(shellLogPath()))
	w("%s %s\n", label("stop", 8), "niq stop")
	w("%s %s\n", label("restart", 8), "niq restart")
	return b.String()
}

// exposure names the bind scope in a word or two: loopback is the safe default,
// so a reader has to be able to recognise it at a glance.
func exposure(bound string) string {
	if helper.IsLoopbackBind(bound) {
		return "loopback only"
	}
	host, _, err := net.SplitHostPort(bound)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return "all interfaces"
	}
	return "on " + host
}

// remoteURL is the address a different machine would open, or "" when this
// start is loopback-only (or no reachable address can be determined). A
// wildcard bind names no host, so a concrete local address stands in.
func remoteURL(bound string) string {
	if helper.IsLoopbackBind(bound) {
		return ""
	}
	host, _, err := net.SplitHostPort(bound)
	if err != nil {
		return ""
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = helper.LanAddr()
	}
	port := helper.Port(bound)
	if host == "" || port == 0 {
		return ""
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}

func printControlStopped() {
	printBanner(banner{
		state: bracketed("gateway") + " " + stateWord(false),
	})
	fmt.Printf("%s %s  %s\n", label("start", 8), "niq", dim("background"))
	fmt.Printf("%s %s  %s\n", label("", 8), "niq -f", dim("this terminal"))
}

// printProjectStatus lists every project with the state of its instance, so
// `niq status` answers "what is actually up right now?" without opening the
// WebUI — the dev loop's usual question when a project page stops responding.
// A project's ports are persisted, so a refused dial means that instance is not
// running: niq reports it as stopped even though the control plane still
// proxies its old port.
//
// controlURL, when the control plane is up, is the address that reaches the
// project from outside. When it is down there is no such address: the project's
// own port speaks JSON only, and its ports are already in their own columns.
func printProjectStatus(controlURL string) {
	views, err := launcher.List()
	if err != nil || len(views) == 0 {
		return
	}
	// Every project gets an address while the gateway is up, running or not:
	// /p/<id>/ serves the shell, with a start page when the instance is down, so
	// the link is worth clicking either way.
	url := func(v contract.View) string {
		if controlURL == "" {
			return ""
		}
		target := contract.ProjectURL(controlURL, v.ID)
		if !v.Running {
			return dim(target)
		}
		return brand(target)
	}
	fmt.Println()
	// The list reports state; acting on it is the picker's job, so the heading
	// says where that is.
	picker.PrintTable(views, url, bold("niq i")+dim(" to start, stop or restart one"))
}

// openInBrowser is best-effort: the address is always printed, so a failed
// opener (headless box, container) is not an error.
func openInBrowser(url string) {
	opener := "open"
	if runtime.GOOS == "linux" {
		opener = "xdg-open"
	}
	_ = exec.Command(opener, url).Start()
}

func printShellStarted(st *shellState) {
	printControlRunning(st)
	printProjectStatus(store.LocalhostURL(st.Bound))
	fmt.Printf("\n%s\n", dim("`niq i` opens the interactive UI · `niq -f` runs it in this terminal"))
}

// logTail returns the last n lines of the control-plane log, so a failed start
// can show the actual reason (typically "address already in use") instead of
// just "exited".
func logTail(n int) string {
	b, err := os.ReadFile(shellLogPath())
	if err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
