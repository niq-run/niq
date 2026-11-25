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

	"github.com/niq-run/niq/app/client/tui/clipboard"
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

func (s *screen) stop() {
	if !s.active {
		return
	}
	fmt.Fprint(s.out, "\x1b[?2031l\x1b[<u\x1b[?2004l\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?25h\x1b[?1049l")
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
	go clipboard.Copy(text)
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

// csiLen is the byte length of the ANSI escape starting at s[i], or 0 when s[i]
// does not start one.
func csiLen(s string, i int) int {
	if i+1 >= len(s) || s[i] != 0x1b || s[i+1] != '[' {
		return 0
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1 - i
		}
	}
	return len(s) - i
}

// sliceStyled returns the cells [from, to) of a styled line. It replays the SGR
// state up to from so a colour or background continues across the cut (a banded
// tool card stays banded), and pads with spaces when the range runs past the
// line's visible width.
func sliceStyled(s string, from, to int) string {
	if to <= from {
		return ""
	}
	width := to - from
	var state, out strings.Builder
	cells := 0
	i := 0
	// Replay everything before from into state: the last escape in wins is the
	// colour at the cut.
	for i < len(s) && cells < from {
		if n := csiLen(s, i); n > 0 {
			state.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		cells += runeCells(r)
		i += size
	}
	emitted := 0
	if cells > from { // a wide rune straddled the left edge
		out.WriteByte(' ')
		emitted = 1
	}
	for i < len(s) && emitted < width {
		if n := csiLen(s, i); n > 0 {
			out.WriteString(s[i : i+n])
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runeCells(r)
		if emitted+w > width {
			break
		}
		out.WriteString(s[i : i+size])
		emitted += w
		i += size
	}
	for emitted < width {
		out.WriteByte(' ')
		emitted++
	}
	res := state.String() + out.String()
	if strings.Contains(res, "\x1b[") {
		res += "\x1b[0m"
	}
	return res
}
