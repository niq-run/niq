package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestMenuDrawMatchesHeight guards the redraw invariant: draw writes exactly as
// many lines as height() claims, so moving the cursor up by height() lands on
// the first line. A multi-line title (version + hints) used to be counted as
// one, which left the old title lines behind on every keypress.
func TestMenuDrawMatchesHeight(t *testing.T) {
	for _, m := range []*menu{
		{title: "single line", options: []string{"a", "b"}},
		{title: "line one\nline two", options: []string{"a", "b", "c"}},
		{title: "line one\nline two", options: []string{"only"}},
		{title: "heading\n", options: []string{"a", "b", "c"}, hint: "press enter"},
		{title: "heading\n", options: []string{"a", "b"}, statusLine: true},
	} {
		var buf bytes.Buffer
		m.draw(&buf)
		if got := strings.Count(buf.String(), "\r\n"); got != m.height() {
			t.Fatalf("title %q with %d options drew %d lines, height() = %d",
				m.title, len(m.options), got, m.height())
		}
	}
}

// TestMenuMoveSkipsDisabled pins the action menu's rule: the cursor never rests
// on an option the instance cannot perform, and a move that only finds disabled
// entries keeps the cursor where it was.
func TestMenuMoveSkipsDisabled(t *testing.T) {
	m := &menu{options: []string{"Start", "Stop", "Restart"}, disabled: []bool{false, true, true}}
	m.move(+1)
	if m.cursor != 0 {
		t.Fatalf("down from Start landed on %d, want 0 (the rest are disabled)", m.cursor)
	}
	m.move(-1)
	if m.cursor != 0 {
		t.Fatalf("up from Start landed on %d, want 0", m.cursor)
	}

	m.disabled = []bool{false, false, false}
	m.move(+2)
	if m.cursor != 2 {
		t.Fatalf("down with everything enabled landed on %d, want 2", m.cursor)
	}
	m.move(+1)
	if m.cursor != 0 {
		t.Fatalf("wrap-around landed on %d, want 0", m.cursor)
	}
}
