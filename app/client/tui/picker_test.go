package tui

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/app/standalone/contract"
)

// TestMenuDrawMatchesHeight guards the redraw invariant: draw writes exactly as
// many lines as height() claims, so moving the cursor up by height() lands on
// the first line. A multi-line title (version + hints) used to be counted as
// one, which left the old title lines behind on every keypress.
func TestMenuDrawMatchesHeight(t *testing.T) {
	for _, m := range []*menu{
		{title: "single line", options: []string{"a", "b"}},
		{title: "line one\nline two", options: []string{"a", "b", "c"}},
		{title: "line one\nline two", options: []string{"only"}},
		{title: "heading\n", options: []string{"a", "b", "c"}, hint: "press enter"},
		{title: "heading\n", options: []string{"a", "b"}, statusLine: true},
	} {
		var buf bytes.Buffer
		m.draw(&buf)
		if got := strings.Count(buf.String(), "\r\n"); got != m.height() {
			t.Fatalf("title %q with %d options drew %d lines, height() = %d",
				m.title, len(m.options), got, m.height())
		}
	}
}

// TestMenuMoveSkipsDisabled pins the action menu's rule: the cursor never rests
// on an option the instance cannot perform, and a move that only finds disabled
// entries keeps the cursor where it was.
func TestMenuMoveSkipsDisabled(t *testing.T) {
	m := &menu{options: []string{"Start", "Stop", "Restart"}, disabled: []bool{false, true, true}}
	m.move(+1)
	if m.cursor != 0 {
		t.Fatalf("down from Start landed on %d, want 0 (the rest are disabled)", m.cursor)
	}
	m.move(-1)
	if m.cursor != 0 {
		t.Fatalf("up from Start landed on %d, want 0", m.cursor)
	}

	m.disabled = []bool{false, false, false}
	m.move(+2)
	if m.cursor != 2 {
		t.Fatalf("down with everything enabled landed on %d, want 2", m.cursor)
	}
	m.move(+1)
	if m.cursor != 0 {
		t.Fatalf("wrap-around landed on %d, want 0", m.cursor)
	}
}

// script turns a key sequence into the reader loop() expects. Running out of
// keys reads as quit, so a loop can never spin forever in a test.
func script(keys ...key) func() key {
	i := 0
	return func() key {
		if i >= len(keys) {
			return keyQuit
		}
		k := keys[i]
		i++
		return k
	}
}

// TestMenuLoop is run() without a terminal: scripted keys drive the same
// select, skip and quit rules the picker uses.
func TestMenuLoop(t *testing.T) {
	m := &menu{title: "pick", options: []string{"a", "b", "c"}, disabled: []bool{false, true, false}}
	got, err := m.loop(script(keyDown, keyEnter), io.Discard)
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got != 2 {
		t.Fatalf("chosen = %d, want 2 (the disabled option is stepped over)", got)
	}

	if got, err := (&menu{title: "one", options: []string{"only"}}).loop(script(keyEnter), io.Discard); err != nil || got != 0 {
		t.Fatalf("enter = %d, %v; want 0, nil", got, err)
	}
	if got, err := (&menu{title: "one", options: []string{"only"}}).loop(script(keyQuit), io.Discard); err != nil || got != -1 {
		t.Fatalf("quit = %d, %v; want -1, nil", got, err)
	}
}

// TestReadKey covers the keymap: both arrow spellings, the readline bindings,
// Enter, quit, and a lone Esc (which must not be mistaken for an arrow).
func TestReadKey(t *testing.T) {
	noDeadline := func(time.Time) error { return nil }
	for _, tc := range []struct {
		in   string
		want key
	}{
		{"j", keyDown}, {"k", keyUp},
		{"\x0e", keyDown}, {"\x10", keyUp}, // Ctrl+N / Ctrl+P
		{"\r", keyEnter}, {"\n", keyEnter},
		{"q", keyQuit}, {"\x03", keyQuit}, // q / Ctrl+C
		{"\x15", keyUp}, {"\x04", keyDown}, // Ctrl+U / Ctrl+D, as in less
		{"\x1b[A", keyUp}, {"\x1b[B", keyDown}, // CSI arrows
		{"\x1bOA", keyUp}, {"\x1bOB", keyDown}, // SS3 arrows
		{"\x1b[1;5A", keyUp}, {"\x1b[1;5B", keyDown}, // modified arrows
		{"\x1b[13u", keyEnter}, // Kitty Enter, if a terminal is left in it
		// A stray Esc is not a quit: an IME cancelling a composition sends one.
		{"\x1b", keyOther},
		{"\x1bx", keyOther},
		{"\x1b[Z", keyOther},
		{"z", keyOther},
	} {
		in := bufio.NewReader(strings.NewReader(tc.in))
		if got := readKey(in, noDeadline); got != tc.want {
			t.Errorf("readKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// flakyReader yields a deadline error once before the real bytes, the way a
// partial multi-byte read can surface.
type flakyReader struct {
	erred bool
	s     string
	pos   int
}

func (r *flakyReader) Read(p []byte) (int, error) {
	if !r.erred {
		r.erred = true
		return 0, os.ErrDeadlineExceeded
	}
	if r.pos >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.pos:])
	r.pos += n
	return n, nil
}

// TestReadKeySurvivesIdleTimeout pins that a read timeout is treated as a wait:
// the picker keeps listening instead of closing.
func TestReadKeySurvivesIdleTimeout(t *testing.T) {
	noDeadline := func(time.Time) error { return nil }
	in := bufio.NewReader(&flakyReader{s: "j"})
	if got := readKey(in, noDeadline); got != keyDown {
		t.Fatalf("after a timeout readKey = %v, want keyDown", got)
	}
}

// TestProjectRowsAlignWideIDs guards the id column: a CJK id and an ASCII one of
// the same width must produce rows of the same display width, or the state and
// port columns shift out of line.
func TestProjectRowsAlignWideIDs(t *testing.T) {
	rows := projectRows(
		[]contract.View{{ID: "值班"}, {ID: "demo"}},
		func(contract.View) string { return "" },
		-1,
	)
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}
	if a, b := displayWidth(rows[0]), displayWidth(rows[1]); a != b {
		t.Fatalf("rows have different widths (%d vs %d):\n%s\n%s", a, b, rows[0], rows[1])
	}
}
