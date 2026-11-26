// The message composer, as a standalone component: multi-line editing, chips for
// large pastes, staged attachments, and the delivery mode.
//
// It owns no terminal and no network state — chat hands it keystrokes and paints
// the rows it returns. That keeps the composer testable on its own and lets it
// grow (paste chips, attachments, modes) without touching the transcript.
//
// Large pastes and attachments are kept out of the edit buffer by value: a paste
// lives behind a single private-use rune in the text, so it deletes and moves as
// one character, and is expanded only when the message is sent.
package tui

import (
	"encoding/base64"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/niq-run/niq/app/client/tui/text"
	"github.com/niq-run/niq/app/standalone/contract"
)

const (
	// tokenBase starts the private-use rune range whose members stand for chips
	// (a large paste) inside the edit buffer. One rune per chip keeps it atomic.
	tokenBase = 0xE000

	maxAttachments = 4
	maxImageBytes  = 5 << 20 // 5 MiB, the WebUI's cap for an inline image
	// A paste becomes a chip at this many lines or cells; below it, it is just
	// text the reader can still see.
	pasteChipLines = 4
	pasteChipCells = 400
)

// attachment is one staged file: an image rides inline as base64, anything else
// is referenced by its path (a local terminal can reach the same disk the
// project runs on, so no upload step is needed).
type attachment struct {
	kind string // "image" | "file"
	name string
	mime string
	path string
	data []byte
	size int64
}

// pasteChip is what a token rune stands for.
type pasteChip struct {
	lines int
	text  string
}

// input is the composer: text, staged attachments and the mode the next message
// is sent with.
type input struct {
	ed     editor
	pastes map[rune]*pasteChip
	next   rune
	attach []attachment
	// mode is the reason worker's input_mode: "" leaves it to the server's
	// default (append), otherwise "interrupt", "append" or "schedule".
	mode string
	// kill is the last text a kill command removed, for ctrl+y (yank).
	kill string
	note string // a one-line note under the text (an attach failure, say)
}

func newInput() *input {
	in := &input{pastes: map[rune]*pasteChip{}, next: tokenBase}
	in.ed.norm()
	return in
}

// modeLabel is how the mode reads in the dock.
func (in *input) modeLabel() string {
	if in.mode == "" {
		return "default"
	}
	return in.mode
}

// ── Editing ──

func (in *input) insert(r rune) {
	if isTokenRune(r) || r == '\n' || r == '\r' || text.IsControl(r) {
		return
	}
	in.ed.insert(r)
}

func (in *input) insertText(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = text.StripEscapes(s)
	for _, r := range s {
		switch {
		case r == '\n':
			in.ed.newline()
		case isTokenRune(r), text.IsControl(r):
			// A chip rune or a control byte from another program: drawing it would
			// corrupt the frame (an escape sequence moves the cursor), so it never
			// reaches the buffer.
		default:
			in.ed.insert(r)
		}
	}
}

// paste stages what the terminal pasted: an existing file path becomes an
// attachment, a large block becomes a chip, and anything else is inserted.
func (in *input) paste(text string) {
	if p := existingPath(text); p != "" {
		in.attachFile(p)
		return
	}
	lines := strings.Count(text, "\n") + 1
	if lines >= pasteChipLines || displayWidth(text) >= pasteChipCells {
		in.addChip(text, lines)
		return
	}
	in.insertText(text)
}

// existingPath reports the file a pasted string names, if it is one: a single
// line that resolves to an existing regular file.
func existingPath(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || strings.ContainsAny(t, "\n") {
		return ""
	}
	t = strings.TrimPrefix(t, "file://")
	if strings.HasPrefix(t, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			t = home + strings.TrimPrefix(t, "~")
		}
	}
	fi, err := os.Stat(t)
	if err != nil || fi.IsDir() {
		return ""
	}
	abs, err := filepath.Abs(t)
	if err != nil {
		return ""
	}
	return abs
}

// attachFile stages one file. Images ride inline (capped); other files keep
// their path, which the project can read directly.
func (in *input) attachFile(path string) {
	if len(in.attach) >= maxAttachments {
		in.note = fmt.Sprintf("at most %d attachments", maxAttachments)
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		in.note = "attach: " + err.Error()
		return
	}
	name := filepath.Base(path)
	if mime := imageMime(path); mime != "" {
		if fi.Size() > maxImageBytes {
			in.note = "image exceeds the 5 MB limit"
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			in.note = "attach: " + err.Error()
			return
		}
		in.attach = append(in.attach, attachment{
			kind: "image", name: name, mime: mime, data: data, size: int64(len(data)),
		})
		in.note = ""
		return
	}
	in.attach = append(in.attach, attachment{kind: "file", name: name, path: path, size: fi.Size()})
	in.note = ""
}

// uploadsFiles reports whether a file attachment has to travel to the gateway
// instead of being referenced by its local path.
//
// A `file` block is only a reference: the reason worker turns it into a note
// naming the path, and a tool later reads that path on the machine running the
// project. So the path is meaningful only when the terminal and the project share
// a disk. A loopback gateway, or the gateway's own hostname, means they do; any
// other address means they might not, and uploading is the only choice that
// still works.
//
// NIQ_ATTACH forces the decision for the cases this cannot see: an SSH tunnel to
// a remote gateway looks local, a shared mount makes a remote host work like a
// local one. "upload" always uploads, "path" always references.
func uploadsFiles(baseURL string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("NIQ_ATTACH"))) {
	case "upload", "remote":
		return true
	case "path", "local":
		return false
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return true // unknown address: prefer the form that always works
	}
	host := u.Hostname()
	if host == "localhost" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	if hn, err := os.Hostname(); err == nil && hn != "" {
		if host == hn || strings.HasPrefix(host, hn+".") {
			return false
		}
	}
	return true
}

// imageMime reports the inline mime for an image extension, or "".
func imageMime(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return ""
}

// addChip hides a large paste behind one token rune.
func (in *input) addChip(text string, lines int) {
	r := in.next
	in.next++
	// Keep the token range from walking into ordinary private-use text forever.
	if in.next-tokenBase > 0x1000 {
		in.next = tokenBase
	}
	in.pastes[r] = &pasteChip{lines: lines, text: text}
	in.ed.insert(r)
}

func isTokenRune(r rune) bool {
	return r >= tokenBase && r < tokenBase+0x1000
}

// chipLabel is the plain text a token shows as.
func (in *input) chipLabel(r rune) string {
	c, ok := in.pastes[r]
	if !ok {
		return "[paste]"
	}
	n := chipNumber(r)
	if c.lines > 1 {
		return fmt.Sprintf("[pasted text #%d · %d lines]", n, c.lines)
	}
	return fmt.Sprintf("[pasted text #%d]", n)
}

func chipNumber(r rune) int { return int(r-tokenBase) + 1 }

// text is the message with every chip expanded to what was pasted.
func (in *input) text() string {
	in.ed.norm()
	parts := make([]string, len(in.ed.lines))
	for i, ln := range in.ed.lines {
		var b strings.Builder
		for _, r := range ln {
			if c, ok := in.pastes[r]; ok {
				b.WriteString(c.text)
				continue
			}
			b.WriteRune(r)
		}
		parts[i] = b.String()
	}
	return strings.Join(parts, "\n")
}

// killEnd / killWord are the readline kill commands. Each records what it removed
// so ctrl+y can put it back. (Killing to the line start is Home then Ctrl+K: Ctrl+U
// pages the transcript, as it does in less and vim.)
func (in *input) killEnd()  { in.kill = in.ed.killToEnd() }
func (in *input) killWord() { in.kill = in.ed.killWord() }

// yank inserts the last killed text at the cursor.
func (in *input) yank() {
	if in.kill != "" {
		in.ed.insertString(in.kill)
	}
}

func (in *input) empty() bool {
	return strings.TrimSpace(in.text()) == "" && len(in.attach) == 0
}

func (in *input) clear() {
	in.ed.clear()
	in.pastes = map[rune]*pasteChip{}
	in.next = tokenBase
	in.attach = nil
	in.note = ""
}

// setDraft puts a message back after a failed send, chips and all.
func (in *input) setDraft(text string) {
	in.ed.clear()
	in.pastes = map[rune]*pasteChip{}
	in.next = tokenBase
	in.ed.setText(text)
}

// blocks turns the composer into the wire content array the WebUI sends: text
// first, then each attachment as an image (base64) or a file (path on disk).
func (in *input) blocks() []contract.Block {
	var out []contract.Block
	if text := in.text(); strings.TrimSpace(text) != "" {
		out = append(out, contract.Block{Type: "text", Text: text})
	}
	for _, a := range in.attach {
		if a.kind == "image" {
			out = append(out, contract.Block{
				Type: "image", Name: a.name, Mime: a.mime,
				Data: base64.StdEncoding.EncodeToString(a.data), Size: a.size,
			})
			continue
		}
		out = append(out, contract.Block{Type: "file", Name: a.name, Path: a.path, Size: a.size})
	}
	return out
}

// ── Rendering ──

// atom is one indivisible piece of a line: a rune, or a chip standing for a
// large paste. Chips never split across rows and count their label's width.
type atom struct {
	text  string
	cells int
	runes int
}

func (in *input) atoms(rs []rune) []atom {
	out := make([]atom, 0, len(rs))
	for _, r := range rs {
		if _, ok := in.pastes[r]; ok {
			label := in.chipLabel(r)
			out = append(out, atom{text: brand(label), cells: displayWidth(label), runes: 1})
			continue
		}
		out = append(out, atom{text: string(r), cells: runeCells(r), runes: 1})
	}
	return out
}

// wrapAtoms groups atoms into rows of at most width cells.
func wrapAtoms(as []atom, width int) [][2]int {
	if len(as) == 0 {
		return [][2]int{{0, 0}}
	}
	var out [][2]int
	start, cells := 0, 0
	for i, a := range as {
		if cells > 0 && cells+a.cells > width {
			out = append(out, [2]int{start, i})
			start, cells = i, 0
		}
		cells += a.cells
	}
	return append(out, [2]int{start, len(as)})
}

// rows lays the composer out as dock rows: the staged attachments (when any),
// then the text wrapped to the available width, with the cursor's position among
// them. The dock scrolls to the cursor when the message outgrows maxInputRows.
func (in *input) rows(width int) (rows []string, curRow, curCol int) {
	avail := width - 2
	if avail < 1 {
		avail = 1
	}
	if len(in.attach) > 0 {
		var b strings.Builder
		for i, a := range in.attach {
			if i > 0 {
				b.WriteString(dim(" · "))
			}
			b.WriteString(brandBold(a.kind) + dim(" ") + a.name)
		}
		rows = append(rows, "  "+clipStyled(b.String(), avail))
	}
	if in.note != "" {
		rows = append(rows, "  "+clipStyled(yellowBright(in.note), avail))
	}

	in.ed.norm()
	for i, ln := range in.ed.lines {
		as := in.atoms(ln)
		groups := wrapAtoms(as, avail)
		runeIdx := 0
		for gi, g := range groups {
			var b strings.Builder
			cell := 0
			for ai := g[0]; ai < g[1]; ai++ {
				a := as[ai]
				if i == in.ed.row && runeIdx == in.ed.col {
					curRow, curCol = len(rows), cell
				}
				b.WriteString(a.text)
				cell += a.cells
				runeIdx += a.runes
			}
			prefix := "  "
			if i == 0 && gi == 0 {
				prefix = brand("› ")
			}
			rows = append(rows, prefix+b.String())
			if i == in.ed.row && runeIdx == in.ed.col {
				curRow, curCol = len(rows)-1, cell
			}
		}
	}
	if len(rows) > maxInputRows {
		top := curRow - maxInputRows + 1
		if top < 0 {
			top = 0
		}
		if top > len(rows)-maxInputRows {
			top = len(rows) - maxInputRows
		}
		rows = rows[top:]
		curRow -= top
	}
	curCol += 2 // the prompt inset
	return rows, curRow, curCol
}

// ── Line editor ──

// editor is the multi-line buffer behind the composer. The message is kept as
// logical lines with a cursor anywhere in them; shift+enter (or ctrl+j) starts a
// new one, enter sends. It knows nothing about pastes, attachments or the
// terminal — the input above owns those.
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
