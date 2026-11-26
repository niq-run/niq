// Package ui holds the small, reusable terminal controls that stand on their
// own, independent of the chat session. Today that is the modal card: a bordered
// panel framed by a band of the accent colour, which the pickers and the help
// page are drawn in and can be clicked around.
//
// Each control is layout only — the owner supplies the body and decides what the
// recorded block means — and depends on ../term for styling and cell maths.
package ui

import (
	"strings"

	"github.com/niq-run/niq/app/client/tui/term"
)

// Dialog is a bordered card: a caption set into the top rule, the body, and a
// caption on the bottom rule. Body rows are already styled.
type Dialog struct {
	Title  string
	Footer string
	Body   []string
}

// Lines lays the card out. It returns the card's rows (no horizontal padding)
// and the box width, so the caller can place the card over the frame. When the
// body would overflow maxRows it is truncated (the last row becomes an ellipsis),
// so a short terminal still shows a usable card.
func (d Dialog) Lines(width, maxRows int) ([]string, int) {
	maxW := width - 2*(FrameLineX+FrameGapX) - 2
	if maxW > 74 {
		maxW = 74
	}
	// A card fits its content: a tight popup leaves more of the frame in view than
	// one stretched to a fixed width.
	need := 0
	for _, ln := range d.Body {
		if w := term.VisibleWidth(ln); w > need {
			need = w
		}
	}
	boxW := need + 4
	if boxW > maxW {
		boxW = maxW
	}
	if boxW < 24 {
		boxW = 24
	}
	inner := boxW - 4 // border, space, text, space, border
	if inner < 10 {
		inner = 10
	}

	body := append([]string(nil), d.Body...)
	cap := maxRows - 2 - CardOverhead
	if cap < 1 {
		cap = 1
	}
	if len(body) > cap {
		body = body[:cap]
		body[len(body)-1] = term.Dim("…")
	}

	lines := make([]string, 0, len(body)+2)
	lines = append(lines, boxBorder(d.Title, boxW, true))
	for _, ln := range body {
		lines = append(lines, boxRow(ln, inner))
	}
	lines = append(lines, boxBorder(d.Footer, boxW, false))
	return lines, boxW
}

// The accent frame around a card: a solid accent border, then a blank gap so the
// frame reads as a line rather than a fill. Terminal cells are taller than they are
// wide — roughly two cells to one row — so the left/right border and gap are two
// cells wide while the top and bottom are one row tall; that gives the four sides
// about the same visual weight. (A one-cell side band reads as too thin, since one
// row is taller than one cell.)
const (
	FrameLineX = 2 // accent frame thickness left/right, in cells
	FrameLineY = 1 // accent frame thickness top/bottom, in rows
	FrameGapX  = 2 // blank cells between the frame and the card
	FrameGapY  = 1 // blank rows between the frame and the card

	// CardOverhead is the rows the frame takes above and below the card.
	CardOverhead = 2*FrameLineY + 2*FrameGapY
)

// ringFill is n cells of the frame: the accent colour as a background behind
// blanks. Without colour it degrades to plain spaces.
func ringFill(n int) string {
	if n <= 0 {
		return ""
	}
	bg := term.BrandBg()
	if bg == "" {
		return strings.Repeat(" ", n)
	}
	return bg + strings.Repeat(" ", n) + "\x1b[0m"
}

// Rect is a half-open block of screen cells (X0/X1 columns, Y0/Y1 rows).
type Rect struct{ X0, Y0, X1, Y1 int }

// Contains reports whether the cell (x, y) is inside the block.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X0 && x < r.X1 && y >= r.Y0 && y < r.Y1
}

// PaintCard overlays a card on a finished frame and returns the block it covers:
// the card inside a one-cell accent line with a breathing gap. The card replaces
// only the rows and columns it needs, so the transcript stays readable around it.
func PaintCard(rows, card []string, boxW, width int) Rect {
	totalW := boxW + 2*FrameGapX + 2*FrameLineX
	totalH := len(card) + CardOverhead
	startX := (width - totalW) / 2
	if startX < 0 {
		startX = 0
	}
	startY := (len(rows) - totalH) / 2
	if startY < 0 {
		startY = 0
	}

	innerW := boxW + 2*FrameGapX
	frameRow := ringFill(innerW)          // the top and bottom lines
	gapRow := strings.Repeat(" ", innerW) // gap rows beside the card
	gap := strings.Repeat(" ", FrameGapX) // gap cells beside the card
	first := FrameLineY + FrameGapY       // the card's first row inside the block
	for i := 0; i < totalH; i++ {
		y := startY + i
		if y < 0 || y >= len(rows) {
			continue
		}
		var mid string
		switch {
		case i < FrameLineY || i >= totalH-FrameLineY:
			mid = frameRow
		case i < first || i >= totalH-first:
			mid = gapRow
		default:
			if ci := i - first; ci >= 0 && ci < len(card) {
				mid = gap + card[ci] + gap
			} else {
				mid = gapRow
			}
		}
		rows[y] = composeRow(rows[y], mid, startX, startX+totalW, width)
	}

	y1 := startY + totalH
	if y1 > len(rows) {
		y1 = len(rows)
	}
	x1 := startX + totalW
	if x1 > width {
		x1 = width
	}
	return Rect{X0: startX, Y0: startY, X1: x1, Y1: y1}
}

// composeRow splices one card row into a rendered row: the cells before start
// and from end onward are kept (ANSI included), and the accent frame fills the
// cells on either side of the card.
func composeRow(base, card string, start, end, width int) string {
	var b strings.Builder
	left := term.Clip(base, start)
	b.WriteString(left)
	if w := term.VisibleWidth(left); w < start {
		b.WriteString(strings.Repeat(" ", start-w))
	}
	b.WriteString(ringFill(FrameLineX))
	b.WriteString(card)
	b.WriteString(ringFill(FrameLineX))
	if end < width {
		b.WriteString(term.Slice(base, end, width))
	}
	return b.String()
}

// boxBorder is the card's top or bottom rule, with a caption set into it.
func boxBorder(caption string, width int, top bool) string {
	left, right := "┌", "┐"
	if !top {
		left, right = "└", "┘"
	}
	label := " " + caption + " "
	fill := width - 2 - term.DisplayWidth(label)
	if fill < 0 {
		fill = 0
	}
	return term.Dim(left + label + strings.Repeat("─", fill) + right)
}

// boxRow wraps one content row in side borders, clipped and padded to the inner
// width.
func boxRow(text string, inner int) string {
	if term.VisibleWidth(text) > inner {
		text = term.Clip(text, inner)
	}
	if w := term.VisibleWidth(text); w < inner {
		text += strings.Repeat(" ", inner-w)
	}
	return term.Dim("│") + " " + text + " " + term.Dim("│")
}
