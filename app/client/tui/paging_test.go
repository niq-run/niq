package tui

import (
	"bufio"
	"strings"
	"testing"
)

// TestPageKeysDecode pins that both encodings of ctrl+u / ctrl+d reach the view as
// paging: the legacy control bytes and the Kitty protocol's CSI form.
func TestPageKeysDecode(t *testing.T) {
	cases := []struct {
		seq  string
		want pressKind
	}{
		{"\x15", pressPageUp},
		{"\x04", pressPageDown},
		{"\x1b[117;5u", pressPageUp},
		{"\x1b[100;5u", pressPageDown},
		{"\x1b[5~", pressPageUp},
		{"\x1b[6~", pressPageDown},
	}
	for _, c := range cases {
		r := bufio.NewReader(strings.NewReader(c.seq))
		first, _, err := r.ReadRune()
		if err != nil {
			t.Fatalf("%q: %v", c.seq, err)
		}
		if got := decodePress(r, first); got.kind != c.want {
			t.Errorf("%q => %v, want %v", c.seq, got.kind, c.want)
		}
	}
}

// TestHintShowsTheMode pins the rule's right side: the vim state and the help
// entry, with no shortcut reminders (the help page holds those).
func TestHintShowsTheMode(t *testing.T) {
	insert := &chat{width: 120, target: "niq", vp: viewport{follow: true}, in: newInput()}
	h := stripANSI(insert.hint())
	if !strings.Contains(h, "insert") || !strings.Contains(h, "help") {
		t.Fatalf("insert hint = %q", h)
	}
	for _, hidden := range []string{"esc normal", "to type", "ctrl+?"} {
		if strings.Contains(h, hidden) {
			t.Fatalf("shortcut reminder %q is still printed: %q", hidden, h)
		}
	}
	normal := &chat{width: 120, target: "niq", vp: viewport{follow: true}, normal: true, in: newInput()}
	if h := stripANSI(normal.hint()); !strings.Contains(h, "normal") {
		t.Fatalf("normal hint = %q", h)
	}

	for _, w := range []int{120, 80, 60, 40, 30} {
		for _, c := range []*chat{insert, normal} {
			c.width = w
			if got := visibleWidth(c.hint()); got > w {
				t.Fatalf("width %d: hint is %d cells", w, got)
			}
			h := stripANSI(c.hint())
			want := "insert"
			if c.normal {
				want = "normal"
			}
			if w >= 40 && !strings.Contains(h, want) {
				t.Fatalf("width %d: no state in hint: %q", w, h)
			}
		}
	}
}

// TestPageFeedback pins that a pager key that cannot move the view says why.
func TestPageFeedback(t *testing.T) {
	c := &chat{vp: viewport{follow: true}, in: newInput()}
	c.vp.resize(10)
	c.lines = []string{"one"}
	c.vp.setTotal(len(c.lines))
	c.nudge(c.vp.pageUp, -1)
	if c.status != "everything fits the screen" || c.statusUntil.IsZero() {
		t.Fatalf("short transcript: status=%q", c.status)
	}

	// A long transcript at the bottom reports the bottom.
	c.lines = make([]string, 100)
	c.vp.setTotal(len(c.lines))
	c.nudge(c.vp.pageDown, +1)
	if c.status != "bottom" {
		t.Fatalf("at bottom: status=%q", c.status)
	}
	// A move that works says nothing.
	c.status = ""
	c.nudge(c.vp.pageUp, -1)
	if c.status != "" {
		t.Fatalf("working page set status=%q", c.status)
	}
}
