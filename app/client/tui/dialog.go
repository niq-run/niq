// Modal cards: the bordered card that the help page and the pickers are drawn
// in, framed by a band of the accent colour. Layout only — each owner supplies
// its body and its keys.
package tui

import "strings"

// dialog is a bordered card: a caption set into the top rule, the body, and a
// caption on the bottom rule. Body rows are already styled.
type dialog struct {
	title  string
	footer string
	body   []string
}

// lines lays the card out. It returns the card's rows (no horizontal padding)
// and the box width, so the caller can place the card over the frame. When the
// body would overflow maxRows it is truncated (the last row becomes an ellipsis),
// so a short terminal still shows a usable card.
func (d dialog) lines(width, maxRows int) ([]string, int) {
	maxW := width - 2*(frameLineX+frameGapX) - 2
	if maxW > 74 {
		maxW = 74
	}
	// A card fits its content: a tight popup leaves more of the frame in view than
	// one stretched to a fixed width.
	need := 0
	for _, ln := range d.body {
		if w := visibleWidth(ln); w > need {
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

	body := append([]string(nil), d.body...)
	cap := maxRows - 2 - cardOverhead
	if cap < 1 {
		cap = 1
	}
	if len(body) > cap {
		body = body[:cap]
		body[len(body)-1] = dim("…")
	}

	lines := make([]string, 0, len(body)+2)
	lines = append(lines, boxBorder(d.title, boxW, true))
	for _, ln := range body {
		lines = append(lines, boxRow(ln, inner))
	}
	lines = append(lines, boxBorder(d.footer, boxW, false))
	return lines, boxW
}

// The accent frame around a card: a solid accent border, then a blank gap so the
// frame reads as a line rather than a fill. Terminal cells are taller than they are
// wide — roughly two cells to one row — so the left/right border and gap are two
// cells wide while the top and bottom are one row tall; that gives the four sides
// about the same visual weight. (A one-cell side band reads as too thin, since one
// row is taller than one cell.)
const (
	frameLineX = 2 // accent frame thickness left/right, in cells
	frameLineY = 1 // accent frame thickness top/bottom, in rows
	frameGapX  = 2 // blank cells between the frame and the card
	frameGapY  = 1 // blank rows between the frame and the card

	// cardOverhead is the rows the frame takes above and below the card.
	cardOverhead = 2*frameLineY + 2*frameGapY
)

// ringFill is n cells of the frame: the accent colour as a background behind
// blanks. Without colour it degrades to plain spaces.
func ringFill(n int) string {
	if n <= 0 {
		return ""
	}
	bg := brandBg()
	if bg == "" {
		return strings.Repeat(" ", n)
	}
	return bg + strings.Repeat(" ", n) + "\x1b[0m"
}

// rect is a half-open block of screen cells (x0/x1 columns, y0/y1 rows).
type rect struct{ x0, y0, x1, y1 int }

func (r rect) contains(x, y int) bool {
	return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1
}

// paintCard overlays a card on a finished frame and returns the block it covers:
// the card inside a one-cell accent line with a breathing gap. The card replaces
// only the rows and columns it needs, so the transcript stays readable around it.
func paintCard(rows, card []string, boxW, width int) rect {
	totalW := boxW + 2*frameGapX + 2*frameLineX
	totalH := len(card) + cardOverhead
	startX := (width - totalW) / 2
	if startX < 0 {
		startX = 0
	}
	startY := (len(rows) - totalH) / 2
	if startY < 0 {
		startY = 0
	}

	innerW := boxW + 2*frameGapX
	frameRow := ringFill(innerW)          // the top and bottom lines
	gapRow := strings.Repeat(" ", innerW) // gap rows beside the card
	gap := strings.Repeat(" ", frameGapX) // gap cells beside the card
	first := frameLineY + frameGapY       // the card's first row inside the block
	for i := 0; i < totalH; i++ {
		y := startY + i
		if y < 0 || y >= len(rows) {
			continue
		}
		var mid string
		switch {
		case i < frameLineY || i >= totalH-frameLineY:
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
	return rect{x0: startX, y0: startY, x1: x1, y1: y1}
}

// composeRow splices one card row into a rendered row: the cells before start
// and from end onward are kept (ANSI included), and the accent frame fills the
// cells on either side of the card.
func composeRow(base, card string, start, end, width int) string {
	var b strings.Builder
	left := clipStyled(base, start)
	b.WriteString(left)
	if w := visibleWidth(left); w < start {
		b.WriteString(strings.Repeat(" ", start-w))
	}
	b.WriteString(ringFill(frameLineX))
	b.WriteString(card)
	b.WriteString(ringFill(frameLineX))
	if end < width {
		b.WriteString(sliceStyled(base, end, width))
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
	fill := width - 2 - displayWidth(label)
	if fill < 0 {
		fill = 0
	}
	return dim(left + label + strings.Repeat("─", fill) + right)
}

// boxRow wraps one content row in side borders, clipped and padded to the inner
// width.
func boxRow(text string, inner int) string {
	if visibleWidth(text) > inner {
		text = clipStyled(text, inner)
	}
	if w := visibleWidth(text); w < inner {
		text += strings.Repeat(" ", inner-w)
	}
	return dim("│") + " " + text + " " + dim("│")
}
