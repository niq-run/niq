// Package screen is the alternate-screen framebuffer.
//
// pi renders a full frame and repaints only the lines that changed; this is the
// same idea without a component tree. The caller composes exactly H lines, and
// Screen diffs them against the previous frame, addressing each changed row with
// a cursor move + line clear. That keeps the fixed bottom (hint + input) cheap to
// repaint on every keystroke and avoids a full-screen flicker on every event.
//
// Lines are clipped to the terminal width here (ANSI-aware) so the terminal never
// soft-wraps one of our rows and pushes the bottom out of place — the same reason
// pi truncates over-wide lines at the renderer rather than re-wrapping.
package screen

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	xterm "golang.org/x/term"

	"github.com/niq-run/niq/app/client/tui/clipboard"
	"github.com/niq-run/niq/app/client/tui/term"
)

// Screen owns the alternate buffer and the previous frame it diffs against.
type Screen struct {
	out    io.Writer
	fd     int
	W, H   int
	prev   []string
	active bool
}

// NewScreen creates a Screen for out, measuring the terminal once.
func NewScreen(out io.Writer, fd int) *Screen {
	s := &Screen{out: out, fd: fd}
	s.Measure()
	return s
}

// Measure refreshes the size from the terminal. A size change drops the previous
// frame so the next render repaints every row.
func (s *Screen) Measure() {
	w, h, err := xterm.GetSize(s.fd)
	if err != nil || w <= 0 || h <= 0 {
		if s.W == 0 {
			s.W = 80
		}
		if s.H == 0 {
			s.H = 24
		}
		return
	}
	if w != s.W || h != s.H {
		s.W, s.H = w, h
		s.prev = nil
	}
}

// Start enters the alternate screen and turns on the input modes the view wants.
func (s *Screen) Start() {
	s.Measure()
	// Alternate screen, cursor hidden, screen cleared, home; enable button and
	// drag reporting in SGR form (1000 + 1002 + 1006) so the wheel scrolls and a
	// left drag can select text. 1000 guarantees press/release; 1002 adds motion
	// while a button is held, which is the drag that draws a selection. The Kitty
	// keyboard protocol's disambiguate flag makes shift+enter tell itself apart
	// from enter; terminals that do not know it ignore the sequence. Bracketed
	// paste (2004) delivers a paste as one unit, so a large block can become a
	// chip instead of thousands of keystrokes.
	fmt.Fprint(s.out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H\x1b[?1000h\x1b[?1002h\x1b[?1006h\x1b[?2004h\x1b[>1u")
	// Ask the terminal for its background colour and colour scheme, and subscribe
	// to scheme changes (mode 2031), so the palette matches a light or dark
	// terminal and follows a live toggle. Terminals that do not know these report
	// nothing, and the COLORFGBG/default fallback stands.
	fmt.Fprint(s.out, "\x1b]11;?\x07\x1b[?997n\x1b[?2031h")
	s.prev = nil
	s.active = true
}

// Stop leaves the alternate screen and restores the input modes.
func (s *Screen) Stop() {
	if !s.active {
		return
	}
	fmt.Fprint(s.out, "\x1b[?2031l\x1b[<u\x1b[?2004l\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?25h\x1b[?1049l")
	s.active = false
}

// CopyToClipboard puts text on the system clipboard. OSC 52 reaches terminals
// that support it without an external tool; a native helper covers the rest
// (macOS Terminal, tmux without clipboard passthrough). Both are best-effort.
func (s *Screen) CopyToClipboard(text string) {
	if text == "" || !s.active {
		return
	}
	fmt.Fprintf(s.out, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	go clipboard.Copy(text)
}

// SetTitle sets the terminal window title, which is where the project identity
// lives (the frame itself is reserved for the transcript and the dock).
func (s *Screen) SetTitle(title string) {
	fmt.Fprintf(s.out, "\x1b]0;%s\x07", title)
}

// Render paints lines (clipped to W cells) and places the cursor at (curRow,
// curCol), 0-based. curRow < 0 hides the cursor (the worker picker).
func (s *Screen) Render(lines []string, curRow, curCol int) {
	if s.W <= 0 || s.H <= 0 {
		s.Measure()
	}
	rows := make([]string, s.H)
	for i := 0; i < s.H && i < len(lines); i++ {
		rows[i] = term.Clip(lines[i], s.W)
	}

	full := s.prev == nil || len(s.prev) != s.H
	var b strings.Builder
	b.WriteString("\x1b[?2026h") // begin synchronized update
	if full {
		for r := 0; r < s.H; r++ {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, rows[r])
		}
	} else {
		for r := 0; r < s.H; r++ {
			if rows[r] == s.prev[r] {
				continue
			}
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, rows[r])
		}
	}
	if curRow >= 0 && curRow < s.H {
		if curCol < 0 {
			curCol = 0
		}
		if curCol >= s.W {
			curCol = s.W - 1
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", curRow+1, curCol+1)
	} else {
		b.WriteString("\x1b[?25l")
	}
	b.WriteString("\x1b[?2026l") // end synchronized update
	io.WriteString(s.out, b.String())
	s.prev = rows
}
