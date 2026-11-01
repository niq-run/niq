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
	} {
		var buf bytes.Buffer
		m.draw(&buf)
		if got := strings.Count(buf.String(), "\r\n"); got != m.height() {
			t.Fatalf("title %q with %d options drew %d lines, height() = %d",
				m.title, len(m.options), got, m.height())
		}
	}
}
