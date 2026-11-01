// Project views and the interactive picker: `niq project` lists every project
// (state, ports, directory), lets you move with the arrow keys, then offers the
// actions that make sense for that instance — start when it is stopped, stop or
// restart when it is running.
//
// The menu is a few dozen lines of escape codes rather than a TUI dependency:
// two short blocks, redrawn in place.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/project"
)

// errNotTerminal means stdin is not a TTY (piped input, cron, a script), so
// there is nobody to press arrow keys.
var errNotTerminal = errors.New("stdin is not a terminal")

type key int

const (
	keyOther key = iota
	keyUp
	keyDown
	keyEnter
	keyQuit
)

// menu is one in-place selector: a title line plus options, redrawn on every
// keypress so the block stays where it started instead of scrolling.
type menu struct {
	title   string
	options []string
	cursor  int
}

// run returns the chosen index, or -1 when the user quits (q, Esc, Ctrl+C).
func (m *menu) run() (int, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return -1, errNotTerminal
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return -1, err
	}
	defer term.Restore(fd, state)

	in := bufio.NewReader(os.Stdin)
	m.draw(os.Stdout)
	for {
		switch readKey(in) {
		case keyUp:
			m.cursor = (m.cursor - 1 + len(m.options)) % len(m.options)
			m.draw(os.Stdout)
		case keyDown:
			m.cursor = (m.cursor + 1) % len(m.options)
			m.draw(os.Stdout)
		case keyEnter:
			m.erase(os.Stdout)
			return m.cursor, nil
		case keyQuit:
			m.erase(os.Stdout)
			return -1, nil
		}
	}
}

// draw rewrites the menu block in place. Raw mode means every newline has to
// be an explicit "\r\n"; ERASE_LINE (\x1b[2K) wipes the previous, longer option
// text before the new one is written.
func (m *menu) draw(out io.Writer) {
	fmt.Fprintf(out, "\x1b[%dA\r", m.height()) // back to the title line
	fmt.Fprintf(out, "\x1b[2K%s\r\n", m.title)
	for i, opt := range m.options {
		marker := "   "
		if i == m.cursor {
			marker = cyan(" > ")
		}
		fmt.Fprintf(out, "\x1b[2K%s%s\r\n", marker, opt)
	}
}

// erase removes the block, leaving the cursor where the title started, so the
// result of the action is printed in its place.
func (m *menu) erase(out io.Writer) {
	fmt.Fprintf(out, "\x1b[%dA\r\x1b[J", m.height())
}

func (m *menu) height() int { return len(m.options) + 1 }

// readKey maps one keypress (or one arrow-key escape sequence) to a key.
func readKey(in *bufio.Reader) key {
	b, err := in.ReadByte()
	if err != nil {
		return keyQuit
	}
	switch b {
	case 3, 'q', 'Q': // Ctrl+C
		return keyQuit
	case '\r', '\n':
		return keyEnter
	case 'k':
		return keyUp
	case 'j':
		return keyDown
	case 0x1b:
		// Arrows arrive as ESC [ A / ESC [ B in a single burst, but a lone Esc
		// means quit — wait a moment for the rest before deciding.
		_ = os.Stdin.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
		next, err := in.ReadByte()
		_ = os.Stdin.SetReadDeadline(time.Time{})
		if err != nil {
			return keyQuit
		}
		if next != '[' && next != 'O' {
			return keyQuit
		}
		arrow, err := in.ReadByte()
		if err != nil {
			return keyOther
		}
		switch arrow {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		}
	}
	return keyOther
}

// runProjectPicker is what `niq project` (and `niq project list`) does: pick an
// instance, then an action. Without a terminal it degrades to the plain table,
// so piping the command still means something.
func runProjectPicker() error {
	projects, err := project.ListProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Println(dim("(no projects)"))
		fmt.Printf("\n%s\n", dim("create one with `niq project create <id>`"))
		return nil
	}

	chosen, err := (&menu{
		title:   pickerTitle(),
		options: projectRows(projects, projectDirCell),
	}).run()
	if errors.Is(err, errNotTerminal) {
		printProjectTable(projects, projectDirCell)
		return nil
	}
	if err != nil {
		return err
	}
	if chosen < 0 {
		return nil
	}
	return actOnProject(projects[chosen])
}

func pickerTitle() string {
	return versionTag() + "\n" + dim("↑/↓ move · enter select · q quit")
}

// actOnProject asks what to do with one instance. The choice follows its state:
// a stopped project can be started, a running one stopped or restarted.
func actOnProject(p project.Project) error {
	running := projectRunning(p)
	options := []string{"Restart", "Stop"}
	if !running {
		options = []string{"Start"}
	}
	title := versionTag() + "\n" +
		fmt.Sprintf("%s %s %s", stateDot(running), bold(p.ID), stateWord(running))
	chosen, err := (&menu{title: title, options: options}).run()
	if err != nil || chosen < 0 {
		return err
	}

	switch options[chosen] {
	case "Start":
		return startProjectInstance(p)
	case "Stop":
		return stopProjectInstance(p)
	default:
		return restartProjectInstance(p)
	}
}

// projectRow is the shared line shape: state dot, id, state, ports, and one
// trailing cell — the WebUI URL in `niq status`, the project directory in the
// picker and listing.
func projectRow(p project.Project, idWidth int, trailing string) string {
	running := projectRunning(p)
	row := fmt.Sprintf("%s %s  %s  %s %-5s  %s %-5s  %s",
		stateDot(running),
		bold(fmt.Sprintf("%-*s", idWidth, p.ID)),
		stateWord(running),
		dim("webui"), portCell(p.Ports.WebUI),
		dim("bus"), portCell(p.Ports.Bus),
		trailing,
	)
	return strings.TrimRight(row, " ")
}

func projectRows(projects []project.Project, trailing func(project.Project) string) []string {
	idWidth := 2
	for _, p := range projects {
		if len(p.ID) > idWidth {
			idWidth = len(p.ID)
		}
	}
	rows := make([]string, len(projects))
	for i, p := range projects {
		rows[i] = projectRow(p, idWidth, trailing(p))
	}
	return rows
}

func projectDirCell(p project.Project) string { return dim(shortPath(project.ProjectDir(p.ID))) }

// printProjectTable is the non-interactive view: `niq project` without a
// terminal.
func printProjectTable(projects []project.Project, trailing func(project.Project) string) {
	fmt.Println(dim("projects"))
	for _, row := range projectRows(projects, trailing) {
		fmt.Printf("  %s\n", row)
	}
}

// stopProjectInstance interrupts the instance (SIGINT: it snapshots on the way
// out) and waits for the port to be released. Working through the port rather
// than the control plane's child table keeps it valid for instances started by
// hand, too.
func stopProjectInstance(p project.Project) error {
	if !projectRunning(p) {
		fmt.Printf("%s is %s\n", bold(p.ID), dim("not running"))
		return nil
	}
	if project.InterruptPortHolders(p.Ports.WebUI) == 0 {
		return fmt.Errorf("%s: nothing is listening on port %d", p.ID, p.Ports.WebUI)
	}
	if err := waitPortClosed(p.Ports.WebUI, 15*time.Second); err != nil {
		return fmt.Errorf("%s: %w", p.ID, err)
	}
	fmt.Printf("%s %s\n", bold(p.ID), green("stopped"))
	return nil
}

// startProjectInstance brings a stopped project up, through the control plane
// when there is one (so it owns the instance) and otherwise directly.
func startProjectInstance(p project.Project) error {
	if st, ok := readControlState(); ok && isRunning(st) {
		if err := startProjectViaControl(st, p); err != nil {
			return err
		}
		return nil
	}
	return startProjectDetached(p)
}

// restartProjectInstance restarts through the control plane when it is up (the
// instance then belongs to it, and the WebUI's own restart keeps working), and
// otherwise does the same work locally: stop, wait for the port, start detached.
func restartProjectInstance(p project.Project) error {
	if st, ok := readControlState(); ok && isRunning(st) {
		return restartProjectViaControl(st, p)
	}
	if projectRunning(p) {
		if err := stopProjectInstance(p); err != nil {
			return err
		}
	}
	return startProjectDetached(p)
}

func startProjectViaControl(st *controlState, p project.Project) error {
	out, err := controlProjectCall(st, p.ID, "start")
	if err != nil {
		return err
	}
	if out.WebUIPort == 0 {
		return fmt.Errorf("%s did not come up; see %s", p.ID, shortPath(project.LogPath(p.ID)))
	}
	fmt.Printf("%s %s %s\n", bold(p.ID), green("started"), cyan(projectURL(st, p.ID, out.WebUIPort)))
	return nil
}

func restartProjectViaControl(st *controlState, p project.Project) error {
	out, err := controlProjectCall(st, p.ID, "restart")
	if err != nil {
		return err
	}
	if out.WebUIPort == 0 {
		return fmt.Errorf("%s did not come back up; see %s", p.ID, shortPath(project.LogPath(p.ID)))
	}
	fmt.Printf("%s %s %s\n", bold(p.ID), green("restarted"), cyan(projectURL(st, p.ID, out.WebUIPort)))
	return nil
}

// controlProjectPort is the slice of the control plane's project answer the CLI
// cares about.
type controlProjectPort struct {
	WebUIPort int `json:"webui_port"`
}

// controlProjectCall posts to /api/projects/<id>/<action> and decodes the port
// the answer reports. Both handlers block until the instance is serving.
func controlProjectCall(st *controlState, id, action string) (controlProjectPort, error) {
	var out controlProjectPort
	url := fmt.Sprintf("http://%s/api/projects/%s/%s", probeAddr(st.Bound), id, action)
	client := &http.Client{Timeout: 40 * time.Second}
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return out, fmt.Errorf("control: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return out, fmt.Errorf("control: %s", strings.TrimSpace(string(body)))
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out, nil
}

// startProjectDetached starts an instance of its own, for when there is no
// control plane to ask.
func startProjectDetached(p project.Project) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	cmd := exec.Command(exe, "project", "run", p.ID)
	cmd.SysProcAttr = detachedProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", p.ID, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		if cur, err := project.LoadProject(p.ID); err == nil && cur.Ports.WebUI != 0 &&
			isServing(fmt.Sprintf("127.0.0.1:%d", cur.Ports.WebUI)) {
			fmt.Printf("%s %s %s\n", bold(p.ID), green("started"),
				cyan(fmt.Sprintf("http://localhost:%d", cur.Ports.WebUI)))
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not start within 20s; see %s", p.ID, shortPath(project.LogPath(p.ID)))
		}
		select {
		case err := <-exited:
			return fmt.Errorf("%s exited during startup: %v; see %s", p.ID, err, shortPath(project.LogPath(p.ID)))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// projectURL prefers the control plane's proxied address, which is the one that
// works from elsewhere.
func projectURL(st *controlState, id string, webuiPort int) string {
	if st != nil {
		return strings.TrimSuffix(project.LocalhostURL(st.Bound), "/") + "/p/" + id + "/"
	}
	return fmt.Sprintf("http://localhost:%d", webuiPort)
}

func waitPortClosed(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isServing(fmt.Sprintf("127.0.0.1:%d", port)) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("port %d is still in use after %s", port, timeout)
}
