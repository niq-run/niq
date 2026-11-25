// The alternate-screen framebuffer.
//
// pi renders a full frame and repaints only the lines that changed; this is the
// same idea without a component tree. The chat composes exactly h lines, and
// screen diffs them against the previous frame, addressing each changed row with
// a cursor move + line clear. That keeps the fixed bottom (hint + input) cheap to
// repaint on every keystroke and avoids a full-screen flicker on every event.
//
// Lines are clipped to the terminal width here (ANSI-aware) so the terminal
// never soft-wraps one of our rows and pushes the bottom out of place — the same
// reason pi truncates over-wide lines at the renderer rather than re-wrapping.
package tui

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

type screen struct {
	out    io.Writer
	fd     int
	w, h   int
	prev   []string
	active bool
}

func newScreen(out io.Writer, fd int) *screen {
	s := &screen{out: out, fd: fd}
	s.measure()
	return s
}

// measure refreshes the size from the terminal. A size change drops the previous
// frame so the next render repaints every row.
func (s *screen) measure() {
	w, h, err := term.GetSize(s.fd)
	if err != nil || w <= 0 || h <= 0 {
		if s.w == 0 {
			s.w = 80
		}
		if s.h == 0 {
			s.h = 24
		}
		return
	}
	if w != s.w || h != s.h {
		s.w, s.h = w, h
		s.prev = nil
	}
}

func (s *screen) start() {
	s.measure()
	// Alternate screen, cursor hidden, screen cleared, home; enable button and
	// drag reporting in SGR form (1000 + 1002 + 1006) so the wheel scrolls and a
	// left drag can select text. 1000 guarantees press/release; 1002 adds motion
	// while a button is held, which is the drag that draws a selection.
	fmt.Fprint(s.out, "\x1b[?1049h\x1b[?25l\x1b[2J\x1b[H\x1b[?1000h\x1b[?1002h\x1b[?1006h")
	s.prev = nil
	s.active = true
}

func (s *screen) stop() {
	if !s.active {
		return
	}
	fmt.Fprint(s.out, "\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?25h\x1b[?1049l")
	s.active = false
}

// copyToClipboard puts text on the system clipboard. OSC 52 reaches terminals
// that support it without an external tool; a native helper covers the rest
// (macOS Terminal, tmux without clipboard passthrough). Both are best-effort.
func (s *screen) copyToClipboard(text string) {
	if text == "" || !s.active {
		return
	}
	fmt.Fprintf(s.out, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	go nativeCopy(text)
}

// setTitle sets the terminal window title, which is where the project identity
// lives (the frame itself is reserved for the transcript and the dock).
func (s *screen) setTitle(title string) {
	fmt.Fprintf(s.out, "\x1b]0;%s\x07", title)
}

// render paints lines (clipped to w cells) and places the cursor at (curRow,
// curCol), 0-based. curRow < 0 hides the cursor (the worker picker).
func (s *screen) render(lines []string, curRow, curCol int) {
	if s.w <= 0 || s.h <= 0 {
		s.measure()
	}
	rows := make([]string, s.h)
	for i := 0; i < s.h && i < len(lines); i++ {
		rows[i] = clipStyled(lines[i], s.w)
	}

	full := s.prev == nil || len(s.prev) != s.h
	var b strings.Builder
	b.WriteString("\x1b[?2026h") // begin synchronized update
	if full {
		for r := 0; r < s.h; r++ {
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, rows[r])
		}
	} else {
		for r := 0; r < s.h; r++ {
			if rows[r] == s.prev[r] {
				continue
			}
			fmt.Fprintf(&b, "\x1b[%d;1H\x1b[2K%s", r+1, rows[r])
		}
	}
	if curRow >= 0 && curRow < s.h {
		if curCol < 0 {
			curCol = 0
		}
		if curCol >= s.w {
			curCol = s.w - 1
		}
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", curRow+1, curCol+1)
	} else {
		b.WriteString("\x1b[?25l")
	}
	b.WriteString("\x1b[?2026l") // end synchronized update
	io.WriteString(s.out, b.String())
	s.prev = rows
}

// clipStyled truncates s to at most width terminal cells, copying ANSI escape
// sequences through without counting them and closing any style it cut off.
func clipStyled(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	cells := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				if s[j] >= 0x40 && s[j] <= 0x7e {
					j++
					break
				}
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runeCells(r)
		if cells+w > width {
			break
		}
		cells += w
		b.WriteString(s[i : i+size])
		i += size
	}
	if i < len(s) {
		b.WriteString("\x1b[0m")
	}
	return b.String()
}
