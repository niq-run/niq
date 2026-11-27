package ui

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/term"
)

func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				if s[j] >= 0x40 && s[j] <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestComposeRowKeepsTheSides pins the splice itself: the card replaces only
// [start, end), with the accent frame each side, and the row's own text survives
// outside it.
func TestComposeRowKeepsTheSides(t *testing.T) {
	end := 3 + 2*FrameLineX + 3 // left + frame + box + frame
	got := stripANSI(composeRow("01234567890123", "BOX", 3, end, 14))
	if want := "012  BOX  0123"; got != want {
		t.Fatalf("composeRow = %q, want %q", got, want)
	}
}

// TestCardRingIsBlue pins the frame: with colour on, the gutter around a card is
// the accent background, and the card's own lines carry no background.
func TestCardRingIsBlue(t *testing.T) {
	oldAnsi, oldTC := term.Ansi, term.Truecolor
	defer func() { term.Ansi, term.Truecolor = oldAnsi, oldTC }()
	term.Ansi, term.Truecolor = true, true

	if got := ringFill(3); !strings.Contains(got, "48;2;60;120;180") {
		t.Fatalf("ringFill lost the accent background: %q", got)
	}
	rows := []string{strings.Repeat("x", 40)}
	card := []string{"┌ foo ┐", "└─────┘"}
	r := PaintCard(rows, card, 7, 40)
	if !strings.Contains(rows[0], "48;2;60;120;180") {
		t.Fatalf("the frame is not drawn around the card: %q", rows[0])
	}
	if strings.Contains(card[0], "48;2;60;120;180") {
		t.Fatal("the frame leaked into the card's own rows")
	}
	if want := (40 - (7 + 2*FrameGapX + 2*FrameLineX)) / 2; r.X0 != want {
		t.Fatalf("rect x0 = %d, want %d", r.X0, want)
	}
}
