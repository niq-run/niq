package tui

import (
	"bufio"
	"strings"
	"testing"
)

// TestDecodeTerminalReports pins that both reply spellings are consumed as a
// report — not typed into the composer, and not mistaken for a key. (The palette
// adaptation itself is tested in the term package.)
func TestDecodeTerminalReports(t *testing.T) {
	for _, c := range []struct {
		seq  string
		want string
	}{
		{"\x1b]11;rgb:1e1e/1e1e/1e1e\x07", "11;rgb:1e1e/1e1e/1e1e"},
		{"\x1b]11;rgb:ff/ff/ff\x1b\\", "11;rgb:ff/ff/ff"},
		{"\x1b[?997;2n", "997;2"},
	} {
		r := bufio.NewReader(strings.NewReader(c.seq))
		first, _, err := r.ReadRune()
		if err != nil {
			t.Fatalf("%q: %v", c.seq, err)
		}
		k := decodePress(r, first)
		if k.Kind != pressTerminal || k.Text != c.want {
			t.Fatalf("%q => %+v, want report %q", c.seq, k, c.want)
		}
	}
}
