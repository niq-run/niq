package tui

import (
	"bufio"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/text"
)

// TestEmacsC0Keys pins the readline bindings a text field is expected to honour:
// ctrl+a/e/b/f/p/n move, ctrl+d deletes, ctrl+k/u/w kill, ctrl+y yanks.
func TestEmacsC0Keys(t *testing.T) {
	cases := []struct {
		r    rune
		want pressKind
	}{
		{0x01, pressHome}, {0x05, pressEnd},
		{0x02, pressLeft}, {0x06, pressRight},
		{0x10, pressPrevUser}, {0x0e, pressNextUser},
		{0x0b, pressKillEnd}, {0x17, pressKillWord},
		{0x19, pressYank},
		// Ctrl+U / Ctrl+D page the transcript, as in less and vim.
		{0x15, pressPageUp}, {0x04, pressPageDown},
		{0x03, pressInterrupt},
		{0x11, pressInterrupt}, // alternate quit
	}
	for _, c := range cases {
		got, ok := controlPress(c.r)
		if !ok || got.Kind != c.want {
			t.Errorf("controlPress(%#x) = %v, %v; want %v", c.r, got.Kind, ok, c.want)
		}
	}
	// Only Ctrl+U / Ctrl+D page; the emacs editing keys stay with the editor.
	for _, r := range []rune{0x06, 0x02, 0x19, 0x05, 0x01, 0x0b} {
		if p, ok := controlPress(r); ok && (p.Kind == pressPageUp || p.Kind == pressPageDown) {
			t.Errorf("controlPress(%#x) pages but should edit", r)
		}
	}
}

// TestModifiedArrows pins the transcript bindings that replaced the ctrl keys:
// shift+arrow scrolls a line, ctrl+arrow pages, plain arrows move the editor.
func TestModifiedArrows(t *testing.T) {
	cases := []struct {
		seq  string
		want pressKind
	}{
		{"\x1b[A", pressUp},
		{"\x1b[1;2A", pressLineUp},
		{"\x1b[1;2B", pressLineDown},
		{"\x1b[1;5A", pressPageUp},
		{"\x1b[1;5B", pressPageDown},
	}
	for _, c := range cases {
		r := bufio.NewReader(strings.NewReader(c.seq))
		first, _, _ := r.ReadRune()
		if got := decodePress(r, first); got.Kind != c.want {
			t.Errorf("%q => %v, want %v", c.seq, got.Kind, c.want)
		}
	}
	// alt+b / alt+f are word motions.
	r := bufio.NewReader(strings.NewReader("\x1bb"))
	first, _, _ := r.ReadRune()
	if got := decodePress(r, first); got.Kind != pressAlt || got.R != 'b' {
		t.Errorf("alt+b => %+v", got)
	}
}

// TestEditorKillYank pins the kill commands and the yank that restores them,
// including a kill that swallows a newline.
func TestEditorKillYank(t *testing.T) {
	in := newInput()
	in.ed.setText("alpha beta")
	in.ed.end()
	in.killWord()
	if in.ed.text() != "alpha " || in.kill != "beta" {
		t.Fatalf("killWord: text=%q kill=%q", in.ed.text(), in.kill)
	}
	in.yank()
	if in.ed.text() != "alpha beta" {
		t.Fatalf("yank: %q", in.ed.text())
	}

	in.ed.setText("keep this tail")
	in.ed.home()
	in.ed.col = 5 // "keep "
	in.killEnd()
	if in.ed.text() != "keep " || in.kill != "this tail" {
		t.Fatalf("killEnd: text=%q kill=%q", in.ed.text(), in.kill)
	}

	// At the end of a line, killEnd takes the newline and joins.
	in.ed.setText("one\ntwo")
	in.ed.row, in.ed.col = 0, 3
	if got := in.ed.killToEnd(); got != "\n" || in.ed.text() != "onetwo" {
		t.Fatalf("kill newline: %q text=%q", got, in.ed.text())
	}
}

// TestKillStartKeys pins that Cmd+Backspace (macOS) and Ctrl+Backspace
// (Windows/Linux) decode to the delete-to-line-start key, while a bare backspace
// still deletes one character. KitTy encodes the modifiers in `CSI 127;mod u` and
// modifyOtherKeys in `CSI 27;mod;127 ~`; mod is bits+1, super=8 so Cmd=9, ctrl=4
// so Ctrl=5.
func TestKillStartKeys(t *testing.T) {
	for _, mod := range []int{5, 9} { // ctrl+backspace, cmd+backspace
		if p := keyPress(127, mod); p.Kind != pressKillStart {
			t.Errorf("keyPress(127,%d) = %v, want killStart", mod, p.Kind)
		}
		if p := keyPress(8, mod); p.Kind != pressKillStart {
			t.Errorf("keyPress(8,%d) = %v, want killStart", mod, p.Kind)
		}
	}
	if p := keyPress(127, 1); p.Kind != pressBackspace {
		t.Errorf("plain backspace decoded as %v, want pressBackspace", p.Kind)
	}
	// KitTy and modifyOtherKeys wire spellings.
	for _, seq := range []string{"\x1b[127;9u", "\x1b[27;9;127~"} {
		r := bufio.NewReader(strings.NewReader(seq))
		first, _, _ := r.ReadRune()
		if got := decodePress(r, first); got.Kind != pressKillStart {
			t.Errorf("%q => %v, want killStart", seq, got.Kind)
		}
	}
}

// TestKillStartEdits pins the editor move itself and the end-to-end key: the
// draft's line is cleared up to the cursor and ctrl+y puts it back.
func TestKillStartEdits(t *testing.T) {
	in := newInput()
	in.ed.setText("alpha beta")
	in.ed.end()
	in.killStart()
	if in.ed.text() != "" || in.kill != "alpha beta" {
		t.Fatalf("killStart: text=%q kill=%q", in.ed.text(), in.kill)
	}
	in.yank()
	if in.ed.text() != "alpha beta" {
		t.Fatalf("yank after killStart: %q", in.ed.text())
	}

	// Only the part before the cursor goes.
	in.ed.setText("one two three")
	in.ed.home()
	in.ed.col = 7 // "one two"
	if got := in.ed.killToStart(); got != "one two" || in.ed.text() != " three" {
		t.Fatalf("killToStart mid-line: removed=%q text=%q", got, in.ed.text())
	}

	// Through the keymap: the composer clears, not just one rune.
	c, _ := newTestChat(t)
	c.in.setDraft("hello")
	c.handleKey(t.Context(), press{Kind: pressKillStart}, nil)
	if c.in.text() != "" {
		t.Fatalf("kill-start key left %q", c.in.text())
	}
}

// TestWordMotions pins alt+b / alt+f movement over letters, digits and underscore.
func TestWordMotions(t *testing.T) {
	in := newInput()
	in.ed.setText("foo_bar baz")
	in.ed.end()
	in.ed.wordLeft()
	if in.ed.col != 8 {
		t.Fatalf("wordLeft col=%d, want 8", in.ed.col)
	}
	in.ed.wordLeft()
	if in.ed.col != 0 {
		t.Fatalf("wordLeft col=%d, want 0", in.ed.col)
	}
	in.ed.wordRight()
	if in.ed.col != 7 {
		t.Fatalf("wordRight col=%d, want 7", in.ed.col)
	}
}

// TestSanitizeDisplay pins that control bytes cannot corrupt the frame: escapes
// become dots, a carriage return and bell too, a tab becomes spaces, and the
// newline that carries structure survives.
func TestSanitizeDisplay(t *testing.T) {
	got := text.Sanitize("a\x1b[2Jb\rc\x07d\te\nf")
	want := "a·[2Jb·c·d    e\nf"
	if got != want {
		t.Fatalf("sanitizeDisplay = %q, want %q", got, want)
	}
	if plain := text.Sanitize("nothing to do"); plain != "nothing to do" {
		t.Fatalf("clean text changed: %q", plain)
	}
	if strings.Contains(text.Sanitize("x\x1b[31my"), "\x1b") {
		t.Fatal("escape survived")
	}
}

// TestInputDropsControlBytes pins that the composer never stores a control byte,
// so a hostile paste cannot reach the frame or the model.
func TestInputDropsControlBytes(t *testing.T) {
	in := newInput()
	// Whole escape sequences are removed, not just their ESC: leaving the tail
	// behind would turn a pasted colour code into visible junk.
	in.insertText("a" + "\x1b[31m" + "b\tc\x07d")
	if got := in.text(); got != "ab\tcd" {
		t.Fatalf("text = %q", got)
	}
	if strings.ContainsRune(in.text(), 0x1b) || strings.ContainsRune(in.text(), 0x07) {
		t.Fatal("control byte reached the buffer")
	}
}
