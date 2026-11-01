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
	"errors"
	"fmt"
	"io"
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
// be an explicit "\r\n" — including the ones inside a multi-line title, which
// is why the title is split rather than printed as-is; ERASE_LINE (\x1b[2K)
// wipes the previous, longer text before the new one is written.
func (m *menu) draw(out io.Writer) {
	fmt.Fprintf(out, "\x1b[%dA\r", m.height()) // back to the title line
	for _, line := range strings.Split(m.title, "\n") {
		fmt.Fprintf(out, "\x1b[2K%s\r\n", line)
	}
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

// height is how many terminal lines the block occupies. The title may be more
// than one line (version + hint), so both draw and the cursor-up in erase count
// title lines — otherwise every redraw leaves the top lines behind.
func (m *menu) height() int { return len(m.options) + 1 + strings.Count(m.title, "\n") }

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

// startProjectInstance brings a stopped project up. It does not go through the
// control plane: an instance is identified by its ports, so starting one is the
// same operation whether the plane is running or not — and the plane picks it
// up on the next probe.
func startProjectInstance(p project.Project) error {
	if projectRunning(p) {
		fmt.Printf("%s is %s %s\n", bold(p.ID), green("already running"),
			cyan(fmt.Sprintf("http://localhost:%d", p.Ports.WebUI)))
		return nil
	}
	return startProjectDetached(p)
}

// restartProjectInstance stops the instance through its port and starts a new
// one on the same addresses — exactly what the WebUI's restart does.
func restartProjectInstance(p project.Project) error {
	if projectRunning(p) {
		if err := stopProjectInstance(p); err != nil {
			return err
		}
	}
	return startProjectDetached(p)
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
