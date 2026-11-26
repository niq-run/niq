package tui

import (
	"bufio"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/layout"
	"github.com/niq-run/niq/app/client/tui/screen"
)

// TestModalModes pins the vim split: esc goes normal, i comes back, and in normal
// mode letters scroll instead of typing.
func TestModalModes(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, seen: map[string]bool{}}
	c.width, c.height = 40, 10
	c.lines = make([]string, 100)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))

	// Insert mode: typing goes into the message.
	c.insertKey(t.Context(), press{Kind: pressRune, R: 'j'}, nil)
	if got := c.in.text(); got != "j" {
		t.Fatalf("insert mode did not type: %q", got)
	}
	c.in.clear()

	// esc → normal; 'j' then scrolls and types nothing.
	c.handleKey(t.Context(), press{Kind: pressEsc}, nil)
	if !c.normal {
		t.Fatal("esc did not enter normal mode")
	}
	top := c.vp.First()
	c.handleKey(t.Context(), press{Kind: pressRune, R: 'k'}, nil)
	if c.in.text() != "" {
		t.Fatalf("normal mode typed: %q", c.in.text())
	}
	if c.vp.First() == top {
		t.Fatal("normal mode k did not scroll up")
	}

	// 'i' returns to insert mode.
	c.handleKey(t.Context(), press{Kind: pressRune, R: 'i'}, nil)
	if c.normal {
		t.Fatal("i did not leave normal mode")
	}
}

// TestClickPicksTheMode pins the mouse rule: the transcript is normal mode, the
// composer is insert.
func TestClickPicksTheMode(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, seen: map[string]bool{}}
	c.width, c.height = 40, 12
	c.lines = make([]string, 4)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))

	// A click in the transcript area (row 1) enters normal mode.
	c.handleKey(t.Context(), press{Kind: pressClick, X: 3, Y: 2}, nil)
	if !c.normal {
		t.Fatal("clicking the transcript did not enter normal mode")
	}
	// A click in the dock (bottom row) returns to insert.
	c.handleKey(t.Context(), press{Kind: pressClick, X: 3, Y: c.height}, nil)
	if c.normal {
		t.Fatal("clicking the composer did not enter insert mode")
	}
}

// TestHelpPopup pins the popup: `?` opens it in normal mode, any key closes it,
// and it is drawn over the transcript.
func TestHelpPopup(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, seen: map[string]bool{}, normal: true}
	c.width, c.height = 70, 24
	c.lines = []string{"transcript line"}
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))

	c.handleKey(t.Context(), press{Kind: pressRune, R: '?'}, nil)
	if !c.help {
		t.Fatal("? did not open help")
	}
	// Any key closes it, and that key does not also act.
	c.handleKey(t.Context(), press{Kind: pressRune, R: 'j'}, nil)
	if c.help {
		t.Fatal("key did not close help")
	}
	if !c.normal {
		t.Fatal("closing help switched out of normal mode")
	}

	// The box itself: bordered, capped in width, and listing both modes.
	box := helpBox(70, 40)
	plain := stripANSI(strings.Join(box, "\n"))
	for _, want := range []string{"help", "insert", "normal", "esc or click outside", "ctrl+t / ctrl+v"} {
		if !strings.Contains(plain, want) {
			t.Errorf("help box missing %q:\n%s", want, plain)
		}
	}
	if !strings.HasPrefix(strings.TrimLeft(plain, " "), "┌ help") {
		t.Errorf("box not centered: %q", plain[:20])
	}
	for _, ln := range box {
		if w := visibleWidth(ln); w > 70 {
			t.Fatalf("box row is %d cells wide", w)
		}
	}

	// The card is an overlay: it covers its own rows, so the transcript stays
	// readable above and below it on a frame taller than the card.
	c.help = true
	buf := new(strings.Builder)
	scr := screen.NewScreen(buf, 0)
	scr.W, scr.H = 80, 40
	c.scr, c.width, c.height = scr, 80, 40
	c.render()
	frame := buf.String()
	if !strings.Contains(frame, "transcript line") {
		t.Fatal("the help card hid the whole transcript instead of overlaying it")
	}
	if !strings.Contains(frame, "┌") {
		t.Fatal("help page did not paint the box")
	}
}

// TestNormalModeDoesNotTypeTheHelpKey pins that '?' is text in insert mode.
func TestNormalModeDoesNotTypeTheHelpKey(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}}
	c.insertKey(t.Context(), press{Kind: pressRune, R: '?'}, nil)
	if got := c.in.text(); got != "?" {
		t.Fatalf("insert mode lost the question mark: %q", got)
	}
	if c.help {
		t.Fatal("insert mode opened help")
	}
}

// TestNormalModeControlKeys pins the vim paging bindings in normal mode.
func TestNormalModeControlKeys(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true}
	c.width, c.height = 40, 10
	c.lines = make([]string, 100)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))

	before := c.vp.First()
	// ctrl+u arrives as a page-up press; at the bottom it is the one that moves.
	c.handleKey(t.Context(), press{Kind: pressPageUp}, nil)
	if c.vp.First() == before {
		t.Fatal("normal mode ctrl+u did not page up")
	}
	// ctrl+b / ctrl+f arrive as left / right.
	mid := c.vp.First()
	c.handleKey(t.Context(), press{Kind: pressLeft}, nil)
	if c.vp.First() == mid {
		t.Fatal("normal mode ctrl+b did not page up")
	}
}

// TestNoTypeInNormalMode pins that editing keys do nothing while normal.
func TestNoTypeInNormalMode(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true}
	c.in.ed.setText("draft")
	c.handleKey(t.Context(), press{Kind: pressBackspace}, nil)
	c.handleKey(t.Context(), press{Kind: pressKillEnd}, nil)
	if got := c.in.text(); got != "draft" {
		t.Fatalf("normal mode edited the message: %q", got)
	}
}

// silence the unused-import check when bufio is not needed by a given test set.
var _ = bufio.NewReader

// TestNormalModeScrollsOnce pins the regression: a paging key must move the view
// one page in normal mode, not two (the shared switch and the mode handler both
// used to act on it).
func TestNormalModeScrollsOnce(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true}
	c.width, c.height = 40, 12
	c.lines = make([]string, 100)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))
	page := c.vp.VisibleHeight()

	c.handleKey(t.Context(), press{Kind: pressPageUp}, nil)
	if got, want := c.vp.First(), 100-page*2; got != want {
		t.Fatalf("page up moved to %d, want %d (moved %d pages)", got, want, (100-got)/page)
	}
}

// TestNormalModeEnterDoesNotSend pins that enter is not a send while the view has
// the keys: it scrolls a line, as in vim.
func TestNormalModeEnterDoesNotSend(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true, seen: map[string]bool{}}
	c.width, c.height = 40, 12
	c.lines = make([]string, 100)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))
	c.in.ed.setText("do not send me")

	c.handleKey(t.Context(), press{Kind: pressEnter}, nil)
	if c.in.text() != "do not send me" {
		t.Fatalf("normal-mode enter consumed the draft: %q", c.in.text())
	}
	if c.sends != nil {
		t.Fatal("normal-mode enter queued a send")
	}
}

// TestInsertModeEnterSends pins the other half: enter sends only in insert mode.
func TestInsertModeEnterSends(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, seen: map[string]bool{}}
	c.in.ed.setText("hi")
	// A nil client would panic inside the send goroutine; the draft is cleared
	// before it starts, which is what this asserts.
	c.handleKey(t.Context(), press{Kind: pressEnter}, nil)
	if !c.in.empty() {
		t.Fatalf("insert-mode enter did not take the draft: %q", c.in.text())
	}
}

// TestHelpIsDismissedByAnyKey pins the rule: closing a page never tears down the
// conversation, so even ctrl+c only closes it — a second press is what leaves.
func TestHelpIsDismissedByAnyKey(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, help: true}
	if quit := c.handleKey(t.Context(), press{Kind: pressInterrupt}, nil); quit {
		t.Fatal("ctrl+c quit the chat while help was open")
	}
	if c.help {
		t.Fatal("ctrl+c did not close help")
	}
	// With the page closed, the same key does leave.
	if quit := c.handleKey(t.Context(), press{Kind: pressInterrupt}, nil); !quit {
		t.Fatal("ctrl+c did not quit after help was closed")
	}

	// Esc closes the page and does not also change mode.
	c2 := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, help: true}
	if quit := c2.handleKey(t.Context(), press{Kind: pressEsc}, nil); quit || c2.help {
		t.Fatalf("esc: quit=%v help=%v", quit, c2.help)
	}
	if c2.normal {
		t.Fatal("closing help with esc also switched mode")
	}
}

// TestNormalModeLineScroll pins vim's ctrl+e / ctrl+y: one line per press.
func TestNormalModeLineScroll(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true}
	c.width, c.height = 40, 12
	c.lines = make([]string, 100)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))
	bottom := c.vp.First()

	c.handleKey(t.Context(), press{Kind: pressYank}, nil) // ctrl+y
	if got := c.vp.First(); got != bottom-1 {
		t.Fatalf("ctrl+y moved to %d, want %d", got, bottom-1)
	}
	c.handleKey(t.Context(), press{Kind: pressEnd}, nil) // ctrl+e
	if got := c.vp.First(); got != bottom {
		t.Fatalf("ctrl+e moved to %d, want %d", got, bottom)
	}
}

// TestNormalModeExplainsItself pins the feedback for an unbound key: it says how to
// get back to typing instead of doing nothing.
func TestNormalModeExplainsItself(t *testing.T) {
	c := &chat{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true}
	c.handleKey(t.Context(), press{Kind: pressRune, R: 'x'}, nil)
	if !strings.Contains(c.status, "press i") {
		t.Fatalf("no hint for an unbound key: %q", c.status)
	}
	if c.statusUntil.IsZero() {
		t.Fatal("hint does not expire")
	}
}
