package tui

import (
	"strings"
	"testing"
)

// TestSelectRange checks the reverse-video span covers exactly the asked cells and
// leaves the text untouched.
func TestSelectRange(t *testing.T) {
	line := bold("hello world")
	got := selectRange(line, 6, 11)
	if plain := stripANSI(got); plain != "hello world" {
		t.Fatalf("text changed: %q", plain)
	}
	if !strings.Contains(got, "\x1b[7m") {
		t.Fatalf("no reverse span: %q", got)
	}
	// Every reverse opened is closed.
	if strings.Count(got, "\x1b[7m") != strings.Count(got, "\x1b[27m") {
		t.Fatalf("unbalanced reverse: %q", got)
	}
	if !strings.Contains(got, "\x1b[7mworld") {
		t.Fatalf("selection starts on the wrong cell: %q", got)
	}
}

// TestSelectRangeBanded checks reverse is re-asserted after the reset a band
// emits, so the highlight is not cancelled mid-span.
func TestSelectRangeBanded(t *testing.T) {
	line := bandLine("\x1b[1mabc\x1b[0mdef", 6, 0, "\x1b[48;5;22m")
	got := selectRange(line, 0, 6)
	if plain := stripANSI(got); plain != "abcdef" {
		t.Fatalf("text changed: %q", plain)
	}
	if strings.Count(got, "\x1b[7m") < 2 {
		t.Fatalf("reverse not re-asserted after reset: %q", got)
	}
	if !strings.HasSuffix(got, "\x1b[27m\x1b[0m") {
		t.Fatalf("reverse left open: %q", got)
	}
}

// TestSelectionText checks the clipboard text: plain, per line, right-trimmed,
// with no trailing blank line when the drag stops at a line start.
func TestSelectionText(t *testing.T) {
	lines := []string{
		bold("first line"),
		"",
		bandLine("second  ", 8, 0, "\x1b[48;5;22m"),
		"third",
	}

	// A multi-line selection keeps the blank line between the blocks.
	var sel selection
	sel.begin(selPoint{line: 0, col: 6})
	sel.extend(selPoint{line: 2, col: 8})
	if got := selectionText(lines, &sel); got != "line\n\nsecond" {
		t.Fatalf("selectionText = %q", got)
	}

	// Dragging to the start of the next line adds no empty trailing line.
	sel.begin(selPoint{line: 3, col: 0})
	sel.extend(selPoint{line: 4, col: 0})
	if got := selectionText(lines, &sel); got != "third" {
		t.Fatalf("trailing line selection = %q", got)
	}

	// Trailing band padding is trimmed on the right.
	sel.begin(selPoint{line: 2, col: 0})
	sel.extend(selPoint{line: 2, col: 8})
	if got := selectionText(lines, &sel); got != "second" {
		t.Fatalf("padded selection = %q", got)
	}
}

// TestSelectionColumns pins the per-line cell ranges of a multi-line selection.
func TestSelectionColumns(t *testing.T) {
	var sel selection
	sel.begin(selPoint{line: 2, col: 4})
	sel.extend(selPoint{line: 5, col: 3})

	cases := []struct {
		line, start, end int
		ok               bool
	}{
		{1, 0, 0, false},
		{2, 4, 10, true},
		{3, 0, 10, true},
		{5, 0, 3, true},
		{6, 0, 0, false},
	}
	for _, c := range cases {
		start, end, ok := sel.columns(c.line, 10)
		if ok != c.ok || (ok && (start != c.start || end != c.end)) {
			t.Fatalf("columns(%d) = %d,%d,%v want %d,%d,%v",
				c.line, start, end, ok, c.start, c.end, c.ok)
		}
	}

	// Extending backwards swaps the endpoints.
	sel.begin(selPoint{line: 5, col: 3})
	sel.extend(selPoint{line: 2, col: 4})
	if start, end, ok := sel.columns(2, 10); !ok || start != 4 || end != 10 {
		t.Fatalf("reverse columns(2) = %d,%d,%v", start, end, ok)
	}
}
