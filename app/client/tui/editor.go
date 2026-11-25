// The message buffer: logical lines with a cursor, plus the readline operations
// (word motion, kill, yank) the composer's keys drive. It knows nothing about
// pastes, attachments or the terminal — input.go owns those.
package tui

import (
	"strings"
	"unicode"
)

// ── Line editor ──

// editor is the multi-line input at the bottom of the view. The message is kept
// as logical lines with a cursor anywhere in them; shift+enter (or ctrl+j)
// starts a new one, enter sends.
type editor struct {
	lines [][]rune
	row   int
	col   int
}

// norm restores the invariant: at least one line, with the cursor inside it.
func (e *editor) norm() {
	if len(e.lines) == 0 {
		e.lines = [][]rune{{}}
	}
	if e.row < 0 {
		e.row = 0
	}
	if e.row >= len(e.lines) {
		e.row = len(e.lines) - 1
	}
	if e.col < 0 {
		e.col = 0
	}
	if n := len(e.lines[e.row]); e.col > n {
		e.col = n
	}
}

func (e *editor) line() []rune {
	return e.lines[e.row]
}

func (e *editor) insert(r rune) {
	e.norm()
	ln := e.lines[e.row]
	ln = append(ln, 0)
	copy(ln[e.col+1:], ln[e.col:])
	ln[e.col] = r
	e.lines[e.row] = ln
	e.col++
}

// newline splits the current line at the cursor and starts the next one.
func (e *editor) newline() {
	e.norm()
	ln := e.lines[e.row]
	head := append([]rune(nil), ln[:e.col]...)
	tail := append([]rune(nil), ln[e.col:]...)
	e.lines = append(e.lines, nil)
	for i := len(e.lines) - 1; i > e.row+1; i-- {
		e.lines[i] = e.lines[i-1]
	}
	e.lines[e.row] = head
	e.lines[e.row+1] = tail
	e.row++
	e.col = 0
}

func (e *editor) backspace() {
	e.norm()
	if e.col > 0 {
		ln := e.lines[e.row]
		e.lines[e.row] = append(ln[:e.col-1], ln[e.col:]...)
		e.col--
		return
	}
	if e.row == 0 {
		return
	}
	// At the start of a line: join it onto the one above.
	prev := e.lines[e.row-1]
	e.col = len(prev)
	e.lines[e.row-1] = append(prev, e.lines[e.row]...)
	e.lines = append(e.lines[:e.row], e.lines[e.row+1:]...)
	e.row--
}

func (e *editor) delete() {
	e.norm()
	ln := e.lines[e.row]
	if e.col < len(ln) {
		e.lines[e.row] = append(ln[:e.col], ln[e.col+1:]...)
		return
	}
	if e.row+1 >= len(e.lines) {
		return
	}
	// At the end of a line: pull the next one up.
	e.lines[e.row] = append(ln, e.lines[e.row+1]...)
	e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
}

func (e *editor) left() {
	e.norm()
	if e.col > 0 {
		e.col--
		return
	}
	if e.row > 0 {
		e.row--
		e.col = len(e.lines[e.row])
	}
}

func (e *editor) right() {
	e.norm()
	if e.col < len(e.lines[e.row]) {
		e.col++
		return
	}
	if e.row+1 < len(e.lines) {
		e.row++
		e.col = 0
	}
}

func (e *editor) up() {
	e.norm()
	if e.row == 0 {
		return
	}
	e.row--
	if n := len(e.lines[e.row]); e.col > n {
		e.col = n
	}
}

func (e *editor) down() {
	e.norm()
	if e.row+1 >= len(e.lines) {
		return
	}
	e.row++
	if n := len(e.lines[e.row]); e.col > n {
		e.col = n
	}
}

func (e *editor) home() { e.norm(); e.col = 0 }
func (e *editor) end()  { e.norm(); e.col = len(e.lines[e.row]) }

// wordLeft moves to the start of the word before the cursor, on this line.
func (e *editor) wordLeft() {
	e.norm()
	ln := e.lines[e.row]
	for e.col > 0 && !isWordRune(ln[e.col-1]) {
		e.col--
	}
	for e.col > 0 && isWordRune(ln[e.col-1]) {
		e.col--
	}
}

// wordRight moves past the word after the cursor, on this line.
func (e *editor) wordRight() {
	e.norm()
	ln := e.lines[e.row]
	for e.col < len(ln) && !isWordRune(ln[e.col]) {
		e.col++
	}
	for e.col < len(ln) && isWordRune(ln[e.col]) {
		e.col++
	}
}

// isWordRune is what the word motions treat as part of a word: letters, digits
// and the underscore, the convention readline uses.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// killToEnd removes from the cursor to the end of the line (the newline too, when
// the cursor is already there) and returns what it removed.
func (e *editor) killToEnd() string {
	e.norm()
	ln := e.lines[e.row]
	if e.col < len(ln) {
		killed := string(ln[e.col:])
		e.lines[e.row] = ln[:e.col]
		return killed
	}
	if e.row+1 >= len(e.lines) {
		return ""
	}
	e.lines[e.row] = append(ln, e.lines[e.row+1]...)
	e.lines = append(e.lines[:e.row+1], e.lines[e.row+2:]...)
	return "\n"
}

// killWord removes the word before the cursor.
func (e *editor) killWord() string {
	e.norm()
	saved := e.col
	e.wordLeft()
	ln := e.lines[e.row]
	killed := string(ln[e.col:saved])
	e.lines[e.row] = append(ln[:e.col], ln[saved:]...)
	return killed
}

// insertString inserts text at the cursor, splitting on newlines so a yanked
// multi-line block comes back as lines.
func (e *editor) insertString(s string) {
	for _, r := range s {
		if r == '\n' {
			e.newline()
			continue
		}
		e.insert(r)
	}
}

func (e *editor) clear() {
	e.lines = nil
	e.row, e.col = 0, 0
	e.norm()
}

func (e *editor) text() string {
	e.norm()
	parts := make([]string, len(e.lines))
	for i, ln := range e.lines {
		parts[i] = string(ln)
	}
	return strings.Join(parts, "\n")
}

// setText replaces the message (used to restore a draft a failed send gave back)
// and puts the cursor at the end.
func (e *editor) setText(s string) {
	e.lines = nil
	for _, ln := range strings.Split(s, "\n") {
		e.lines = append(e.lines, []rune(ln))
	}
	e.row = len(e.lines) - 1
	e.col = len(e.lines[e.row])
}
