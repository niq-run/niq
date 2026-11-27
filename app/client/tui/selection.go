// Mouse text selection over the transcript, as a standalone component.
//
// The alternate screen captures the mouse, so the terminal's own drag-select no
// longer works. This replaces it: a press anchors a point, drags extend it, and
// the release copies the covered cells. Points are absolute transcript
// coordinates (line index + cell column), not screen rows, so a selection survives
// scrolling and re-wrapping.
//
// It knows nothing about events, workers or the keyboard: it maps cell ranges on
// styled lines and extracts their plain text, so the cell math can be tested on
// its own. The scroll window it sits over is layout.Viewport.
package tui

import (
	"strings"
	"unicode/utf8"
)

// selPoint addresses one cell in the transcript.
type selPoint struct {
	line int
	col  int
}

// selection is an in-progress or finished mouse selection.
type selection struct {
	anchor   selPoint
	cursor   selPoint
	active   bool // a selection exists (possibly empty)
	dragging bool // the pointer moved since the press
}

// begin starts a new selection at p, replacing any previous one.
func (s *selection) begin(p selPoint) {
	s.anchor, s.cursor = p, p
	s.active, s.dragging = true, false
}

// extend moves the selection's free end to p.
func (s *selection) extend(p selPoint) {
	if !s.active {
		return
	}
	if p != s.cursor {
		s.dragging = true
	}
	s.cursor = p
}

// clear forgets the selection.
func (s *selection) clear() { *s = selection{} }

// bounds returns the normalized endpoints: from is inclusive, to is exclusive.
func (s *selection) bounds() (from, to selPoint) {
	if s.anchor.line < s.cursor.line ||
		(s.anchor.line == s.cursor.line && s.anchor.col <= s.cursor.col) {
		return s.anchor, s.cursor
	}
	return s.cursor, s.anchor
}

// empty reports whether the selection covers no cells.
func (s *selection) empty() bool {
	from, to := s.bounds()
	return from == to
}

// columns is the [start, end) cell range selected on one line, and whether that
// line is inside the selection at all. width is the line's cell width, used for
// the open ends of a multi-line selection.
func (s *selection) columns(line, width int) (start, end int, ok bool) {
	if !s.active {
		return 0, 0, false
	}
	from, to := s.bounds()
	if line < from.line || line > to.line {
		return 0, 0, false
	}
	start = 0
	if line == from.line {
		start = from.col
	}
	end = width
	if line == to.line {
		end = to.col
	}
	if end < start {
		end = start
	}
	return start, end, true
}

// selectRange paints cells [from, to) of one styled line with reverse video, so a
// selection is visible over any band without losing the line's own colours. A
// reset inside the span cancels reverse, so it is re-asserted after every escape
// sequence met while inside.
func selectRange(s string, from, to int) string {
	if from >= to {
		return s
	}
	var b strings.Builder
	cell := 0
	inside := false
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
			b.WriteString(s[i:j])
			if inside {
				b.WriteString("\x1b[7m")
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runeCells(r)
		if !inside && cell+w > from && cell < to {
			b.WriteString("\x1b[7m")
			inside = true
		}
		b.WriteString(s[i : i+size])
		cell += w
		i += size
		if inside && cell >= to {
			b.WriteString("\x1b[27m")
			inside = false
		}
	}
	if inside {
		b.WriteString("\x1b[27m")
	}
	return b.String()
}

// plainCells is the unstyled text of cells [from, to) of one styled line, right
// trimmed. That is what a copy puts on the clipboard: no band padding, no colour.
func plainCells(s string, from, to int) string {
	var b strings.Builder
	cell := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			// Skip a CSI or OSC sequence: neither contributes cells.
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) {
					if s[j] >= 0x40 && s[j] <= 0x7e {
						j++
						break
					}
					j++
				}
			} else if j < len(s) && s[j] == ']' {
				for j < len(s) && s[j] != 0x07 {
					j++
				}
				if j < len(s) {
					j++
				}
			} else {
				j++
			}
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		w := runeCells(r)
		if cell >= from && cell+w <= to {
			b.WriteRune(r)
		}
		cell += w
		i += size
	}
	return strings.TrimRight(b.String(), " ")
}

// selectionText flattens a selection into the clipboard's text: one string per
// covered line, joined by newlines. A drag that ends at the very start of a line
// does not add a trailing empty line.
func selectionText(lines []string, sel *selection) string {
	if sel == nil || !sel.active || sel.empty() {
		return ""
	}
	from, to := sel.bounds()
	out := make([]string, 0, to.line-from.line+1)
	for line := from.line; line <= to.line; line++ {
		if line < 0 || line >= len(lines) {
			continue
		}
		start, end, ok := sel.columns(line, visibleWidth(lines[line]))
		if !ok {
			continue
		}
		out = append(out, plainCells(lines[line], start, end))
	}
	if len(out) > 1 && to.col == 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
