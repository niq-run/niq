// The conversation view: `niq c <project>`, or the picker's Chat action. It is
// the terminal's answer to the WebUI's talk view — the same project bus, seen
// from another client.
//
// The scope is deliberately the conversation only: send a message, and show the
// live event stream as it arrives. There is no worker filter and no history
// paging yet — every event is appended top to bottom, whoever it is from. The
// send target is a separate concern: Ctrl+T switches which talk partner the next
// message is addressed to, without narrowing the stream.
//
// Rendering follows picker.go's approach — a few escape codes instead of a TUI
// dependency. The transcript scrolls normally; the single input line is kept at
// the bottom by erasing it before an event is printed and drawing it again after.
package tui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/niq-run/niq/app/standalone/contract"
)

// idleRead is how long a blocking read waits before checking whether the
// conversation ended. A real keypress returns at once; the deadline only bounds
// how long the reader lingers after the view closes, so it never steals a
// keystroke from whatever runs next.
const idleRead = 200 * time.Millisecond

// escRead is the pause that tells a lone Esc from the start of an arrow sequence.
const escRead = 40 * time.Millisecond

// RunChat opens the conversation view for one project on a gateway. baseURL is
// the gateway the caller resolved; the project is reached through it at
// /p/<id>/api/…, never on the project's own data port.
func RunChat(baseURL, projectID string) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errNotTerminal
	}

	client := contract.New(baseURL)
	workers, err := client.Workers(projectID)
	if err != nil {
		return err
	}
	partners := talkPartners(workers)
	if len(partners) == 0 {
		return fmt.Errorf("project %s has no talk partner (a reason, coding or niq worker)", projectID)
	}
	target := pickTarget(partners)

	c := newChat(os.Stdout, termWidth())
	c.partners = partners
	c.target = target

	state, err := term.MakeRaw(fd)
	if err != nil {
		return err
	}
	defer term.Restore(fd, state)

	c.header(projectID, target, len(partners))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := make(chan contract.Event, 64)
	notes := make(chan string, 16)
	keys := make(chan press, 16)
	sends := make(chan sendResult, 4)
	followDone, readDone := make(chan struct{}), make(chan struct{})

	go func() {
		defer close(followDone)
		follow(ctx, client, projectID, events, notes)
	}()
	go func() {
		defer close(readDone)
		readKeys(ctx, keys)
	}()

	// The bottom region (hint + input) is drawn once here; from now on every
	// event and keystroke erases and repaints it, so the hint stays visible.
	c.drawBottom()

loop:
	for {
		select {
		case ev := <-events:
			if r, show := describe(ev, c.width); show {
				c.commit(r)
			}
		case msg := <-notes:
			c.commit(note(msg))
		case res := <-sends:
			if res.err != nil {
				c.commit(note("send failed: " + res.err.Error()))
				// Keep the draft if the user has not started another message, so a
				// refused send (a stopped project, a busy bus) is retryable.
				if c.ed.text() == "" {
					for _, r := range res.text {
						c.ed.insert(r)
					}
				}
				c.redraw()
			}
		case p := <-keys:
			// While the target picker is open it owns the keys; the editor is
			// frozen until a choice is made or it is cancelled.
			if c.selecting {
				switch p.kind {
				case pressInterrupt:
					break loop
				case pressEsc:
					c.selecting = false
					c.redraw()
				case pressUp:
					c.selCur = (c.selCur - 1 + len(c.partners)) % len(c.partners)
					c.redraw()
				case pressDown:
					c.selCur = (c.selCur + 1) % len(c.partners)
					c.redraw()
				case pressEnter:
					chosen := c.partners[c.selCur]
					target = chosen.ID
					c.target = chosen.ID
					c.selecting = false
					c.commit(note("send target → " + workerName(chosen)))
				}
				break
			}
			switch p.kind {
			case pressInterrupt:
				break loop
			case pressTarget:
				if len(c.partners) <= 1 {
					c.commit(note("only one talk partner"))
					break
				}
				c.selecting = true
				c.selCur = indexOfWorker(c.partners, target)
				c.redraw()
			case pressEnter:
				text := strings.TrimSpace(c.ed.text())
				if text == "" {
					break
				}
				// Clear the line at once and send in the background, so a slow
				// publish never freezes the live stream behind it. The target is
				// read now: a switch after this keystroke is for the next message.
				c.ed.clear()
				c.redraw()
				sendTarget := target
				go func(msg, to string) {
					err := client.SendInput(projectID, contract.Input{
						Content: []contract.Block{{Type: "text", Text: msg}},
						Targets: []string{to},
					})
					select {
					case sends <- sendResult{text: msg, err: err}:
					case <-ctx.Done():
					}
				}(text, sendTarget)
			case pressRune:
				c.ed.insert(p.r)
				c.redraw()
			case pressBackspace:
				c.ed.backspace()
				c.redraw()
			case pressDelete:
				c.ed.delete()
				c.redraw()
			case pressLeft:
				c.ed.left()
				c.redraw()
			case pressRight:
				c.ed.right()
				c.redraw()
			case pressHome:
				c.ed.home()
				c.redraw()
			case pressEnd:
				c.ed.end()
				c.redraw()
			case pressClear:
				c.ed.clear()
				c.redraw()
			}
		}
	}

	// Stop the pumps and let the key reader finish before the terminal is
	// restored, so it cannot consume a keystroke meant for whatever runs next.
	cancel()
	<-readDone
	<-followDone
	c.finish()
	return nil
}

// follow keeps a live stream open, reconnecting on drops. A stopped or
// restarting project answers the stream with an error rather than hanging, which
// is exactly the case the backoff is for.
func follow(ctx context.Context, client *contract.Client, projectID string, events chan<- contract.Event, notes chan<- string) {
	backoff := time.Second
	const maxBackoff = 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := client.Follow(ctx, projectID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			notify(ctx, notes, "stream: "+err.Error())
			if !sleep(ctx, backoff) {
				return
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		for {
			ev, err := stream.Next()
			if err != nil {
				stream.Close()
				if ctx.Err() != nil {
					return
				}
				notify(ctx, notes, "stream dropped: "+err.Error())
				break
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				stream.Close()
				return
			}
		}
		if !sleep(ctx, backoff) {
			return
		}
	}
}

func notify(ctx context.Context, notes chan<- string, msg string) {
	select {
	case notes <- msg:
	case <-ctx.Done():
	}
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ── Keys ──

// pressKind is what a keystroke means to the editor. It is separate from
// picker.go's key because the conversation needs the rune itself, not just a
// navigation intent.
type pressKind int

const (
	pressOther pressKind = iota
	pressRune
	pressEnter
	pressBackspace
	pressDelete
	pressLeft
	pressRight
	pressUp
	pressDown
	pressHome
	pressEnd
	pressClear
	pressTarget
	pressEsc
	pressInterrupt
)

type press struct {
	kind pressKind
	r    rune
}

// sendResult is the outcome of one background send, handled back on the main
// loop so it can touch the editor without racing the reader.
type sendResult struct {
	text string
	err  error
}

// readKeys decodes stdin keystrokes onto out until the context ends. Reads use a
// short deadline so the goroutine can notice cancellation while idle; a keypress
// itself returns immediately.
func readKeys(ctx context.Context, out chan<- press) {
	in := bufio.NewReader(os.Stdin)
	for {
		_ = os.Stdin.SetReadDeadline(time.Now().Add(idleRead))
		r, _, err := in.ReadRune()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}
			// EOF or a real read error: leave the conversation.
			select {
			case out <- press{kind: pressInterrupt}:
			case <-ctx.Done():
			}
			return
		}
		_ = os.Stdin.SetReadDeadline(time.Time{})
		select {
		case out <- decodePress(in, r):
		case <-ctx.Done():
			return
		}
	}
}

// decodePress maps one rune to a press, reading the rest of an escape sequence
// when the rune is Esc.
func decodePress(in *bufio.Reader, r rune) press {
	switch r {
	case '\r', '\n':
		return press{kind: pressEnter}
	case 0x7f, 0x08:
		return press{kind: pressBackspace}
	case 0x03, 0x04:
		return press{kind: pressInterrupt}
	case 0x01:
		return press{kind: pressHome}
	case 0x05:
		return press{kind: pressEnd}
	case 0x15:
		return press{kind: pressClear}
	case 0x14: // Ctrl+T
		return press{kind: pressTarget}
	case 0x1b:
		return decodeEscape(in)
	}
	if r >= 0x20 && r != 0x7f {
		return press{kind: pressRune, r: r}
	}
	return press{kind: pressOther}
}

// decodeEscape classifies an escape sequence. A lone Esc (no follow-up within
// escRead) cancels the target picker; an arrow maps to its direction.
func decodeEscape(in *bufio.Reader) press {
	_ = os.Stdin.SetReadDeadline(time.Now().Add(escRead))
	defer os.Stdin.SetReadDeadline(time.Time{})
	next, _, err := in.ReadRune()
	if err != nil {
		return press{kind: pressEsc}
	}
	if next != '[' && next != 'O' {
		return press{kind: pressEsc}
	}
	final, _, err := in.ReadRune()
	if err != nil {
		return press{kind: pressOther}
	}
	switch final {
	case 'A':
		return press{kind: pressUp}
	case 'B':
		return press{kind: pressDown}
	case 'C':
		return press{kind: pressRight}
	case 'D':
		return press{kind: pressLeft}
	case 'H':
		return press{kind: pressHome}
	case 'F':
		return press{kind: pressEnd}
	case '1', '7':
		return tildePress(in, pressHome)
	case '4', '8':
		return tildePress(in, pressEnd)
	case '3':
		return tildePress(in, pressDelete)
	}
	return press{kind: pressOther}
}

// tildePress consumes the "~" that ends a CSI 1~/4~/3~ style key and returns its
// meaning, or "other" when the sequence does not end in a tilde.
func tildePress(in *bufio.Reader, kind pressKind) press {
	if r, _, err := in.ReadRune(); err == nil && r == '~' {
		return press{kind: kind}
	}
	return press{kind: pressOther}
}

// ── Line editor ──

// editor is the single-line input at the bottom of the view.
type editor struct {
	buf []rune
	cur int
}

func (e *editor) insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.cur+1:], e.buf[e.cur:])
	e.buf[e.cur] = r
	e.cur++
}

func (e *editor) backspace() {
	if e.cur == 0 {
		return
	}
	e.buf = append(e.buf[:e.cur-1], e.buf[e.cur:]...)
	e.cur--
}

func (e *editor) delete() {
	if e.cur >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.cur], e.buf[e.cur+1:]...)
}

func (e *editor) left() {
	if e.cur > 0 {
		e.cur--
	}
}

func (e *editor) right() {
	if e.cur < len(e.buf) {
		e.cur++
	}
}

func (e *editor) home() { e.cur = 0 }
func (e *editor) end()  { e.cur = len(e.buf) }

func (e *editor) clear() {
	e.buf = e.buf[:0]
	e.cur = 0
}

func (e *editor) text() string { return string(e.buf) }

// window returns the runes to show so that the cursor stays visible within avail
// cells, plus the cursor's cell offset inside that window. It anchors on the
// cursor: enough text before it to stay in view, then as much after it as fits.
func (e *editor) window(avail int) (start, end, curCells int) {
	if avail < 1 {
		avail = 1
	}
	cellsToCursor := 0
	start = e.cur
	for start > 0 {
		w := runeCells(e.buf[start-1])
		if cellsToCursor+w > avail-1 {
			break
		}
		cellsToCursor += w
		start--
	}
	curCells = cellsToCursor
	end = e.cur
	cells := curCells
	for end < len(e.buf) {
		w := runeCells(e.buf[end])
		if cells+w > avail {
			break
		}
		cells += w
		end++
	}
	return start, end, curCells
}

// ── The view ──

// chat owns the screen: the transcript is printed and never redrawn, and the
// bottom line (input or target picker) is erased and redrawn around every event.
type chat struct {
	out      io.Writer
	width    int
	ed       *editor
	partners []contract.Worker
	// target is the worker the next message is addressed to; the bottom hint
	// shows it, and Ctrl+T changes it.
	target string
	// selecting is the send-target picker: while it is open the bottom line shows
	// the candidates and the keys drive it instead of the editor.
	selecting bool
	selCur    int
	// printed is whether anything has been written yet; it gates the blank line
	// that sets the human's turns apart from what came before them.
	printed bool
	// bottomDrawn is whether the persistent bottom region (hint + input) is on
	// screen; only then can it be erased in place.
	bottomDrawn bool
}

func newChat(out io.Writer, width int) *chat {
	if width <= 0 {
		width = 80
	}
	return &chat{out: out, width: width, ed: &editor{}}
}

// termWidth is the terminal's column count, or 80 when it cannot be read.
func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// header is the one-time banner: who this conversation starts with. The shortcut
// hint is not here — it is pinned above the input instead, so it stays visible
// however long the transcript grows.
func (c *chat) header(projectID, target string, partners int) {
	head := dim("niq chat") + " " + bracketed(projectID) + " " + dim("·") + " " + brandBold(target)
	if partners > 1 {
		head += dim(fmt.Sprintf(" (+%d more)", partners-1))
	}
	fmt.Fprintf(c.out, "%s\r\n", head)
}

// commit prints one block above the bottom region, then repaints the bottom
// region. Only the human's turns get a blank line above them; event rows stay
// packed and a worker's answer follows its events directly.
func (c *chat) commit(r rendered) {
	if c.bottomDrawn {
		c.clearBottom()
	}
	if r.separate && c.printed {
		fmt.Fprint(c.out, "\r\n")
	}
	for _, line := range blockLines(r, c.width) {
		fmt.Fprintf(c.out, "%s\r\n", line)
	}
	c.printed = true
	c.drawBottom()
}

// drawBottom paints the persistent bottom region: a dim shortcut hint naming the
// current send target, then the input line (or the target picker while it is
// open). It assumes the cursor is at the start of the hint line.
func (c *chat) drawBottom() {
	fmt.Fprintf(c.out, "\r\x1b[2K%s\r\n", c.hintLine())
	if c.selecting {
		c.drawSelector()
	} else {
		c.drawInput()
	}
	c.bottomDrawn = true
}

// hintLine is the pinned line above the input: who the message goes to, then the
// keys. The target comes first so a narrow terminal truncates the shortcuts, not
// the addressee.
func (c *chat) hintLine() string {
	head := "→ " + c.target
	if c.target == "" {
		head = "→ ?"
	}
	rest := "  enter send · ctrl+t target · ctrl+c quit"
	avail := c.width - displayWidth(head)
	if avail < 0 {
		return brandBold(truncateCells(head, c.width))
	}
	if displayWidth(rest) > avail {
		if avail <= 1 {
			return brandBold(head)
		}
		rest = truncateCells(rest, avail-1) + "…"
	}
	return brandBold(head) + dim(rest)
}

// clearBottom erases the two bottom lines and leaves the cursor at the start of
// the (upper) hint line, ready for a transcript block to be printed there.
func (c *chat) clearBottom() {
	fmt.Fprint(c.out, "\r\x1b[2K")        // input / selector line
	fmt.Fprint(c.out, "\x1b[1A\r\x1b[2K") // hint line above it
}

// drawInput clears the bottom line and repaints the prompt plus the visible
// window of the input, leaving the cursor where the editor says it is.
func (c *chat) drawInput() {
	const prompt, promptCells = "› ", 2
	avail := c.width - promptCells - 1
	if avail < 4 {
		avail = 4
	}
	start, end, curCells := c.ed.window(avail)
	text := string(c.ed.buf[start:end])
	fmt.Fprintf(c.out, "\r\x1b[2K%s%s", brand(prompt), text)
	if back := displayWidth(text) - curCells; back > 0 {
		fmt.Fprintf(c.out, "\x1b[%dD", back)
	}
}

// drawSelector paints the send-target picker on the bottom line in place of the
// input. It stays on one line so the transcript above it is never disturbed.
func (c *chat) drawSelector() {
	w := c.partners[c.selCur]
	state := ""
	if !w.Online {
		state = dim(" (offline)")
	}
	fmt.Fprintf(c.out, "\r\x1b[2K%s%s%s  %s",
		dim("send to "), brandBold("["+workerName(w)+"]"), state,
		dim(fmt.Sprintf("(%d/%d) ↑/↓ · enter · esc", c.selCur+1, len(c.partners))))
}

// redraw repaints the bottom region in whatever mode is current: the target
// picker while it is open, the input otherwise. Every write to the transcript
// goes through it, so an event arriving mid-selection does not wipe the picker.
func (c *chat) redraw() {
	if c.bottomDrawn {
		c.clearBottom()
	}
	c.drawBottom()
}

// finish erases the bottom region and leaves the cursor on a clean line so the
// shell prompt that follows is not drawn over it.
func (c *chat) finish() {
	if c.bottomDrawn {
		c.clearBottom()
	}
}
