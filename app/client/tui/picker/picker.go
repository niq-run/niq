// Project views and the interactive picker: `niq i` lists every project (state,
// ports, directory), lets you move with the arrow keys, then offers the actions
// that make sense for that instance — chat when it is running, start when it is
// stopped, stop or restart while it is up.
//
// Everything here goes through the gateway's HTTP API, so the terminal is just
// another client: no domain package is imported, and the gateway's address is
// handed in rather than discovered.
//
// The menu is a few dozen lines of escape codes rather than a TUI dependency:
// two short blocks, redrawn in place.
package picker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	xterm "golang.org/x/term"

	"github.com/niq-run/niq/app/client/tui"
	"github.com/niq-run/niq/app/client/tui/stdin"
	"github.com/niq-run/niq/app/client/tui/term"
	"github.com/niq-run/niq/app/standalone/contract"
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
	// status is the one line under the options. It is replaced in place, never
	// appended, so a picker that stays up cannot scroll its own output away;
	// statusLine reserves it (the list keeps it, a one-shot menu does not).
	status     string
	statusLine bool
	// hint is a dim line under the options, for menus whose one option needs a
	// word of explanation ("press enter to start").
	hint string
	// foot is a standing note printed as the block's last line, one blank row under
	// everything else (the interactive shell's ctrl+c notice).
	foot string
	// disabled marks options that are listed but cannot be chosen — Stop and
	// Restart on a project that is not running. They stay visible so the menu
	// always reads the same, and the cursor steps over them.
	disabled []bool
	// keep leaves the block on screen when a choice is made: the project list
	// stays up while the action it opens is chosen.
	keep bool
	// refresh rebuilds the options when the cursor moves: the selection is part
	// of the row text (brackets plus the theme colour), so moving means
	// repainting the rows, not just a marker beside them.
	refresh func(cursor int) []string
	drawn   bool // the block is on screen; only then is the cursor-up meaningful
}

// move steps the cursor (wrapping, stepping over disabled options) and, for a
// self-labelling list, rebuilds the rows so the brackets follow it.
func (m *menu) move(delta int) {
	n := len(m.options)
	for step := 1; step <= n; step++ {
		next := ((m.cursor+delta*step)%n + n) % n
		if next >= len(m.disabled) || !m.disabled[next] {
			m.cursor = next
			break
		}
	}
	if m.refresh != nil {
		m.options = m.refresh(m.cursor)
	}
}

// run returns the chosen index, or -1 when the user quits (q, Esc, Ctrl+C).
func (m *menu) run() (int, error) {
	fd := int(os.Stdin.Fd())
	if !xterm.IsTerminal(fd) {
		return -1, errNotTerminal
	}
	state, err := xterm.MakeRaw(fd)
	if err != nil {
		return -1, err
	}
	defer xterm.Restore(fd, state)

	in := stdin.Stream()
	return m.loop(func() key { return readKey(in, stdin.SetDeadline) }, os.Stdout)
}

// loop is run() with the terminal taken out: the key source and the output are
// arguments, so the redraw, select and quit rules can be tested without a TTY.
func (m *menu) loop(read func() key, out io.Writer) (int, error) {
	m.draw(out)
	for {
		switch read() {
		case keyUp:
			m.move(-1)
			m.draw(out)
		case keyDown:
			m.move(+1)
			m.draw(out)
		case keyEnter:
			if m.cursor < len(m.disabled) && m.disabled[m.cursor] {
				continue // shown, not offered
			}
			if !m.keep {
				m.erase(out)
			}
			return m.cursor, nil
		case keyQuit:
			m.erase(out)
			return -1, nil
		}
	}
}

// draw rewrites the menu block in place. Raw mode means every newline has to
// be an explicit "\r\n" — including the ones inside a multi-line title, which
// is why the title is split rather than printed as-is; ERASE_LINE (\x1b[2K)
// wipes the previous, longer text before the new one is written.
func (m *menu) draw(out io.Writer) {
	if m.drawn {
		fmt.Fprintf(out, "\x1b[%dA\r", m.height()) // back to the title line
	}
	m.drawn = true
	for _, line := range strings.Split(m.title, "\n") {
		fmt.Fprintf(out, "\x1b[2K%s\r\n", line)
	}
	for _, opt := range m.options {
		fmt.Fprintf(out, "\x1b[2K%s\r\n", opt)
	}
	if m.hint != "" {
		fmt.Fprintf(out, "\x1b[2K%s\r\n", m.hint)
	}
	if m.statusLine && m.status != "" {
		fmt.Fprintf(out, "\x1b[2K\r\n")             // blank line before the status
		fmt.Fprintf(out, "\x1b[2K%s\r\n", m.status) // status starts at the left edge
	}
	if m.foot != "" {
		fmt.Fprintf(out, "\x1b[2K\r\n")           // a gap between the list and the note
		fmt.Fprintf(out, "\x1b[2K%s\r\n", m.foot) // the note is the block's last line
	}
}

// erase removes the block, leaving the cursor where the title started, so the
// result of the action is printed in its place.
func (m *menu) erase(out io.Writer) {
	fmt.Fprintf(out, "\x1b[%dA\r\x1b[J", m.height())
}

// height is how many terminal lines the block occupies: the title (which may be
// several lines), the options, and the status line under them. draw and the
// cursor-up in erase share it, so a redraw lands exactly on the first line.
func (m *menu) height() int {
	h := len(m.options) + 1 + strings.Count(m.title, "\n")
	if m.hint != "" {
		h++
	}
	if m.statusLine && m.status != "" {
		h += 2 // blank + status
	}
	if m.foot != "" {
		h += 2 // blank + foot
	}
	return h
}

// readKey maps one keypress (or one arrow-key escape sequence) to a key. The
// deadline is what tells a lone Esc apart from the start of an arrow sequence; it
// is a parameter so a test can pass a no-op.
//
// A stray Esc never quits. Switching or cancelling a Chinese IME sends ESC, and
// treating that as quit closed the picker under the user's hands; only q, Ctrl+C
// and the end of input quit now. An idle read timeout is a wait, not a key, and
// is retried for the same reason.
func readKey(in *bufio.Reader, setDeadline func(time.Time) error) key {
	b, err := in.ReadByte()
	for errors.Is(err, os.ErrDeadlineExceeded) {
		// A timeout, or a partially delivered multi-byte character: keep waiting.
		b, err = in.ReadByte()
	}
	if err != nil {
		return keyQuit // end of input
	}
	switch b {
	case 3, 'q', 'Q': // Ctrl+C
		return keyQuit
	case '\r', '\n':
		return keyEnter
	case 'k', 16: // k, Ctrl+P (readline/Emacs "previous")
		return keyUp
	case 'j', 14: // j, Ctrl+N (readline/Emacs "next")
		return keyDown
	case 21: // Ctrl+U, as in less/vim: move up in the list
		return keyUp
	case 4: // Ctrl+D, as in less/vim: move down in the list
		return keyDown
	case 0x1b:
		return readEscape(in, setDeadline)
	}
	return keyOther
}

// escBurst is how long to wait for the rest of an escape sequence. Arrows arrive
// as one burst, well inside it.
const escBurst = 40 * time.Millisecond

// readEscape consumes an escape sequence and maps the keys the picker uses.
// Anything it does not recognize is ignored rather than treated as a quit: an IME
// cancelling a composition, or any other program writing ESC, must not close the
// picker under the user's hands.
func readEscape(in *bufio.Reader, setDeadline func(time.Time) error) key {
	_ = setDeadline(time.Now().Add(escBurst))
	defer func() { _ = setDeadline(time.Time{}) }()

	next, err := in.ReadByte()
	if err != nil || (next != '[' && next != 'O') {
		return keyOther // a lone Esc, or something that is not a sequence at all
	}
	var params []int
	cur, have := 0, false
	for {
		b, err := in.ReadByte()
		if err != nil {
			return keyOther
		}
		if b >= '0' && b <= '9' {
			cur = cur*10 + int(b-'0')
			have = true
			continue
		}
		if b == ';' || b == ':' {
			params = append(params, cur)
			cur, have = 0, false
			continue
		}
		// b is the final byte.
		if have || len(params) > 0 {
			params = append(params, cur)
		}
		switch b {
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		case 'u':
			// Kitty keyboard protocol, in case a terminal is left in it: Enter
			// arrives as CSI 13u rather than CR.
			if len(params) > 0 && (params[0] == 13 || params[0] == 10) {
				return keyEnter
			}
		}
		return keyOther
	}
}

// RunPicker is `niq i`: pick an instance, then an action. baseURL is the
// gateway the caller resolved, and the address of every project link this
// prints comes from it. header is the control-plane identity block the caller
// renders (the same one a bare `niq` prints); it leads the screen so the picker
// shows the service's basic information above the list. Empty falls back to the
// version line. Without a terminal it degrades to the plain table, so piping the
// command still means something.
func RunPicker(baseURL, header string) error {
	// The list stays up after an action: acting on one project is rarely the
	// whole errand. The block is redrawn in place each round — states reloaded,
	// cursor kept on the project just acted on — and the result of the last
	// action sits on the status line at the bottom, replaced rather than
	// appended, so the screen never scrolls away from the list.
	client := contract.New(baseURL)

	list := &menu{
		title:      pickerTitle(header),
		keep:       true,
		statusLine: true,
		foot:       term.Dim("ctrl+c exits the interactive shell · niq keeps running in the background"),
	}
	for {
		projects, err := client.List()
		if err != nil {
			return err
		}
		if len(projects) == 0 {
			fmt.Println(term.Dim("(no projects)"))
			fmt.Printf("\n%s\n", term.Dim("create one in the web UI: niq w"))
			return nil
		}
		if list.cursor >= len(projects) {
			list.cursor = len(projects) - 1
		}
		list.options = projectRows(projects, projectDirCell, list.cursor)
		list.refresh = func(c int) []string { return projectRows(projects, projectDirCell, c) }
		chosen, err := list.run()
		if errors.Is(err, errNotTerminal) {
			// Nothing to interact with: the same header then the table is the whole
			// answer.
			if top := strings.TrimRight(header, "\n"); top != "" {
				fmt.Println(top)
				fmt.Println()
			}
			PrintTable(projects, projectDirCell, "")
			return nil
		}
		if err != nil {
			return err
		}
		if chosen < 0 {
			return nil
		}

		// The action menu replaces the list for the moment it is open, then the
		// list comes back with the new state and status.
		p := projects[chosen]
		list.erase(os.Stdout)
		list.drawn = false

		action, err := pickAction(p)
		if err != nil {
			return err
		}
		if action == "" {
			continue
		}
		// Chat takes over the terminal for its whole lifetime; when it returns,
		// the list comes back with fresh state and whatever went wrong on the
		// status line.
		if action == "chat" {
			if err := tui.RunChat(baseURL, p.ID); err != nil {
				list.status = term.Red(err.Error())
			}
			continue
		}
		if line, err := runAction(client, baseURL, p, action); err != nil {
			list.status = term.Red(err.Error())
		} else {
			list.status = line
		}
	}
}

// pickerTitle is the block's header: the control-plane identity block the caller
// passed in (the same basic information a bare `niq` prints — or just the version
// line when none was given), a blank line, then the keys and a standing note that
// quitting only closes this shell.
func pickerTitle(header string) string {
	top := strings.TrimRight(header, "\n")
	if top == "" {
		top = versionTag()
	}
	return top + "\n\n" +
		term.Bold("↑/↓") + term.Dim(", ") + term.Bold("⌃n/⌃p") + term.Dim(" or ") + term.Bold("j/k") +
		term.Dim(" move · enter select · q quit")
}

// pickAction asks what to do with one instance. The actions are always listed in
// the same order; on a project that is not running, Chat, Stop and Restart are
// dimmed and the cursor steps over them, so the menu reads the same wherever you
// are. The cursor starts on the first action that instance can perform — Chat
// when it is up, Start when it is not — so Enter always does something sensible.
// An empty action means the user cancelled.
func pickAction(p contract.View) (string, error) {
	running := p.Running
	options := []string{"Chat", "Start", "Stop", "Restart"}
	actions := []string{"chat", "start", "stop", "restart"}
	disabled := []bool{!running, false, !running, !running}

	// No identity line here: this menu replaces the list for a moment, and the
	// project it is about is the whole heading. The trailing newline is the blank
	// line that separates a heading from its options, as in the list.
	title := fmt.Sprintf("%s %s\n", term.BrandBold("["+p.ID+"]"), stateWord(running))
	m := &menu{title: title, disabled: disabled}
	if !running {
		// Only start is on offer, so say what the key does.
		m.hint = term.Dim("press enter to start the project")
	}
	// Start the cursor on the first action that is actually available: on a
	// stopped project Chat is dimmed, and parking on a disabled row would make
	// Enter do nothing until the user moved.
	for i, d := range disabled {
		if !d {
			m.cursor = i
			break
		}
	}
	m.options = actionRows(options, disabled, m.cursor)
	m.refresh = func(c int) []string { return actionRows(options, disabled, c) }
	chosen, err := m.run()
	if err != nil || chosen < 0 {
		return "", err
	}
	return actions[chosen], nil
}

// actionRows renders the actions the way the project list renders projects:
// the cursor dot, then the bracketed name, with the one under the cursor in the
// theme colour. A disabled row is dim throughout and carries a ring instead of
// the filled dot, so it reads as listed-but-unavailable.
func actionRows(options []string, disabled []bool, cursor int) []string {
	rows := make([]string, len(options))
	for i, opt := range options {
		switch {
		case i < len(disabled) && disabled[i]:
			rows[i] = term.Dim("○ ") + term.Dim("["+opt+"]")
		case i == cursor:
			rows[i] = cursorDot(true) + term.Reverse("["+opt+"]")
		default:
			rows[i] = cursorDot(false) + term.Bold("["+opt+"]")
		}
	}
	return rows
}

// cursorDot is the leading marker both menus use: filled in the theme colour
// for the row under the cursor, a dim ring for the rest.
func cursorDot(selected bool) string {
	if selected {
		return term.Brand("●") + " "
	}
	return term.Dim("○") + " "
}

// runAction performs one action and returns the line the picker shows for it.
func runAction(c *contract.Client, baseURL string, p contract.View, action string) (string, error) {
	switch action {
	case "start":
		return startProject(c, baseURL, p)
	case "stop":
		return stopProject(c, p)
	default:
		return restartProject(c, baseURL, p)
	}
}

// projectRow is the shared line shape: cursor dot, id, state, ports, and one
// trailing cell — the WebUI URL in `niq status`, the project directory in the
// picker and listing.
//
// cursor is the row under the picker's cursor, or -1 for a plain listing. The
// leading dot is the cursor itself, not the run state: filled in the theme
// colour on the selected row, a dim ring on the others. A listing has no
// cursor, so it has no dot either — the state is the word after the name.
func projectRow(p contract.View, idWidth int, trailing string, cursor int, selected bool) string {
	running := p.Running
	dot := ""
	if cursor >= 0 {
		dot = cursorDot(selected)
	}
	// Every name is bracketed — the brackets are part of the row, not a cursor
	// — and the selected one alone takes the theme colour. The cell is
	// idWidth+2 wide either way, so the state and ports stay in one column.
	name := term.Bold("["+p.ID+"]") + strings.Repeat(" ", idWidth-term.DisplayWidth(p.ID))
	if selected {
		name = term.BrandBold("["+p.ID+"]") + strings.Repeat(" ", idWidth-term.DisplayWidth(p.ID))
	}
	row := fmt.Sprintf("%s%s  %s  %s %-5s  %s %-5s  %s",
		dot,
		name,
		stateWord(running),
		term.Dim("webui"), portCell(p.Ports.WebUI),
		term.Dim("bus"), portCell(p.Ports.Bus),
		trailing,
	)
	return strings.TrimRight(row, " ")
}

// projectRows builds the table. cursor is the row under the picker's cursor;
// printProjectTable passes -1, which draws no cursor column at all.
func projectRows(projects []contract.View, trailing func(contract.View) string, cursor int) []string {
	idWidth := 2
	for _, p := range projects {
		if w := term.DisplayWidth(p.ID); w > idWidth {
			idWidth = w
		}
	}
	rows := make([]string, len(projects))
	for i, p := range projects {
		rows[i] = projectRow(p, idWidth, trailing(p), cursor, i == cursor)
	}
	return rows
}

func projectDirCell(p contract.View) string { return term.Dim(shortPath(p.Dir)) }

// PrintTable is the non-interactive view: the picker without a terminal, and
// the list under `niq status`. note, when set, is printed after the heading —
// the status report uses it to say where the list can be acted on, which the
// picker itself does not need to explain.
func PrintTable(projects []contract.View, trailing func(contract.View) string, note string) {
	head := bracketed("projects")
	if note != "" {
		head += "  " + note
	}
	fmt.Println(head)
	for _, row := range projectRows(projects, trailing, -1) {
		fmt.Printf("%s\n", row)
	}
}

// stopProject, startProject and restartProject print one line each. They are
// thin over the client because the gateway already owns the lifecycle; the
// picker only decides how the result reads.
func stopProject(c *contract.Client, p contract.View) (string, error) {
	if err := c.Stop(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s", term.Bold(p.ID), term.Green("stopped")), nil
}

func startProject(c *contract.Client, baseURL string, p contract.View) (string, error) {
	inst, err := c.Start(p.ID)
	if err != nil {
		return "", err
	}
	url := term.Brand(contract.ProjectURL(baseURL, p.ID))
	if inst.Already {
		return fmt.Sprintf("%s is %s %s", term.Bold(p.ID), term.Green("already running"), url), nil
	}
	return fmt.Sprintf("%s %s %s", term.Bold(p.ID), term.Green("started"), url), nil
}

func restartProject(c *contract.Client, baseURL string, p contract.View) (string, error) {
	if _, err := c.Restart(p.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s %s", term.Bold(p.ID), term.Green("restarted"), term.Brand(contract.ProjectURL(baseURL, p.ID))), nil
}
