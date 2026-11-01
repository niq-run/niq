// Interactive project picker: `niq project` lists every project, lets you move
// with the arrow keys, then asks what to do with the chosen one (stop or
// restart). It is deliberately a few dozen lines of escape codes instead of a
// TUI dependency — the whole UI is two short menus drawn in place.
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
			marker = " > "
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

// runProjectPicker is what a bare `niq project` does: pick an instance, then an
// action. Without a terminal it degrades to the plain listing, so piping the
// command still means something.
func runProjectPicker() error {
	projects, err := project.ListProjects()
	if err != nil {
		return err
	}
	if len(projects) == 0 {
		fmt.Println("(no projects)")
		return nil
	}

	chosen, err := (&menu{
		title:   "select a project  (↑/↓ move, enter select, q quit)",
		options: projectMenuOptions(projects),
	}).run()
	if errors.Is(err, errNotTerminal) {
		printProjectList(projects)
		fmt.Println("\n(the picker needs a terminal)")
		return nil
	}
	if err != nil {
		return err
	}
	if chosen < 0 {
		return nil
	}
	p := projects[chosen]

	action, err := (&menu{
		title:   fmt.Sprintf("%s is %s — what next?  (↑/↓ move, enter run, q cancel)", p.ID, projectState(p)),
		options: []string{"Stop", "Restart"},
	}).run()
	if err != nil || action < 0 {
		return err
	}
	if action == 0 {
		return stopProjectInstance(p)
	}
	return restartProjectInstance(p)
}

func projectMenuOptions(projects []project.Project) []string {
	opts := make([]string, len(projects))
	for i, p := range projects {
		opts[i] = fmt.Sprintf("%-16s %s", p.ID, projectState(p))
	}
	return opts
}

func projectState(p project.Project) string {
	if projectRunning(p) {
		return "running"
	}
	return "stopped"
}

// stopProjectInstance interrupts the instance (SIGINT: it snapshots on the way
// out) and waits for the port to be released. Working through the port rather
// than the control plane's child table keeps it valid for instances started by
// hand, too.
func stopProjectInstance(p project.Project) error {
	if !projectRunning(p) {
		fmt.Printf("%s is not running\n", p.ID)
		return nil
	}
	if project.InterruptPortHolders(p.Ports.WebUI) == 0 {
		return fmt.Errorf("%s: nothing is listening on port %d", p.ID, p.Ports.WebUI)
	}
	if err := waitPortClosed(p.Ports.WebUI, 15*time.Second); err != nil {
		return fmt.Errorf("%s: %w", p.ID, err)
	}
	fmt.Printf("%s stopped\n", p.ID)
	return nil
}

// restartProjectInstance restarts through the control plane when it is up (the
// instance then belongs to it, and the WebUI's own restart keeps working), and
// otherwise does the same work locally: stop, wait for the port, start detached.
func restartProjectInstance(p project.Project) error {
	if st, ok := readControlState(); ok && isRunning(st) {
		return restartProjectViaControl(st, p.ID)
	}
	if projectRunning(p) {
		if err := stopProjectInstance(p); err != nil {
			return err
		}
	}
	return startProjectDetached(p)
}

func restartProjectViaControl(st *controlState, id string) error {
	url := fmt.Sprintf("http://%s/api/projects/%s/restart", probeAddr(st.Bound), id)
	client := &http.Client{Timeout: 40 * time.Second} // the handler waits for the new instance
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("control: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("control: %s", strings.TrimSpace(string(body)))
	}
	var out struct {
		WebUIPort int `json:"webui_port"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.WebUIPort == 0 { // the handler answers 200 even when the launch failed
		return fmt.Errorf("%s did not come back up; see %s", id, project.LogPath(id))
	}
	fmt.Printf("%s restarted at %s\n", id, projectURL(st, id, out.WebUIPort))
	return nil
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
			fmt.Printf("%s started at http://localhost:%d\n", p.ID, cur.Ports.WebUI)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not start within 20s; see %s", p.ID, project.LogPath(p.ID))
		}
		select {
		case err := <-exited:
			return fmt.Errorf("%s exited during startup: %v; see %s", p.ID, err, project.LogPath(p.ID))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// projectURL prefers the control plane's proxied address, which is the one that
// works from elsewhere; the loopback port is the fallback when it is down.
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
