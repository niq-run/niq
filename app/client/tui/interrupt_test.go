package tui

import (
	"bufio"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/layout"
)

// TestInterruptedEscapeHandsBackKey pins the fix for ctrl+c being swallowed: a
// stale ESC (an IME cancelling a composition, a terminal reply) followed by a
// control byte must not consume that byte as part of an escape sequence.
func TestInterruptedEscapeHandsBackKey(t *testing.T) {
	for _, c := range []struct {
		name string
		in   string
	}{
		{"lone esc then ctrl+c", "\x1b\x03"},
		{"csi prefix then ctrl+c", "\x1b[\x03"},
		{"osc prefix then ctrl+c", "\x1b]\x03"},
	} {
		r := bufio.NewReader(strings.NewReader(c.in))
		first, _, err := r.ReadRune()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		_ = decodePress(r, first) // the broken escape: consumed, nothing bound
		second, _, err := r.ReadRune()
		if err != nil {
			t.Fatalf("%s: ctrl+c was swallowed: %v", c.name, err)
		}
		if got := decodePress(r, second); got.Kind != pressInterrupt {
			t.Fatalf("%s: second key = %v, want interrupt", c.name, got.Kind)
		}
	}
}

// TestOneInterruptLeavesTheChat pins that a single ctrl+c asks to leave in every
// resting state: insert, normal, and with a picker open (a picker is a menu, not
// a page, so ctrl+c quits rather than only closing it).
func TestOneInterruptLeavesTheChat(t *testing.T) {
	for _, c := range []*chat{
		{in: newInput(), vp: layout.Viewport{Follow: true}},
		{in: newInput(), vp: layout.Viewport{Follow: true}, normal: true},
		{in: newInput(), vp: layout.Viewport{Follow: true}, menu: menuMode},
	} {
		if !c.handleKey(t.Context(), press{Kind: pressInterrupt}, nil) {
			t.Fatal("ctrl+c did not ask to leave")
		}
	}
}

// TestPasteKeepsControlBytes pins that a genuine paste still carries what was in
// it: only *stray* control bytes abort a sequence.
func TestPasteKeepsControlBytes(t *testing.T) {
	body := "a\x03b\tc\nd"
	r := bufio.NewReader(strings.NewReader("\x1b[200~" + body + "\x1b[201~"))
	first, _, _ := r.ReadRune()
	got := decodePress(r, first)
	if got.Kind != pressPaste || got.Text != body {
		t.Fatalf("paste = %+v, want %q", got, body)
	}
}
