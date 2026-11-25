// GFM pipe-table rendering for the terminal.
//
// A table is drawn as a full grid — an outer border, a rule between every pair of
// rows, and a │ between columns — and laid out to the terminal width: columns are
// sized to their content, then shrunk — widest first — until the table fits. Cell
// text wraps inside its column, so a long cell grows its row in height rather than
// pushing the table past the screen. Cells keep inline markdown (bold, code,
// links).
package tui

import "strings"

// isDelimRow reports whether s is a GFM table delimiter row: pipe-separated cells
// of dashes, optionally with alignment colons.
func isDelimRow(s string) bool {
	if !strings.Contains(s, "|") || !strings.Contains(s, "-") {
		return false
	}
	cells := splitRow(s)
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		c = strings.TrimSpace(c)
		c = strings.TrimPrefix(c, ":")
		c = strings.TrimSuffix(c, ":")
		if c == "" {
			return false
		}
		for _, r := range c {
			if r != '-' {
				return false
			}
		}
	}
	return true
}

// splitRow splits a table row into trimmed cells, tolerating the optional leading
// and trailing pipes.
func splitRow(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// alignOf reads a delimiter row's alignment colons into per-column codes:
// 0 left, 1 center, 2 right.
func alignOf(delim string) []int {
	cells := splitRow(delim)
	aligns := make([]int, len(cells))
	for i, c := range cells {
		c = strings.TrimSpace(c)
		left := strings.HasPrefix(c, ":")
		right := strings.HasSuffix(c, ":")
		switch {
		case left && right:
			aligns[i] = 1
		case right:
			aligns[i] = 2
		}
	}
	return aligns
}

// renderTable draws a full grid — an outer border, a rule under the header and
// between every pair of body rows, and │ between columns — fitted to width.
func renderTable(header []string, align []int, rows [][]string, width int) []string {
	n := len(header)
	if n == 0 {
		return nil
	}
	norm := func(r []string) []string {
		out := make([]string, n)
		copy(out, r)
		return out
	}
	header = norm(header)
	for i := range rows {
		rows[i] = norm(rows[i])
	}

	// Header cells are bold; data cells keep their own inline styles.
	hdr := make([][]tok, n)
	widths := make([]int, n)
	for k := 0; k < n; k++ {
		hdr[k] = tokensFromRuns(inlineRuns(header[k]), mdBold)
		widths[k] = tokensWidth(hdr[k])
	}
	body := make([][][]tok, len(rows))
	for r := range rows {
		body[r] = make([][]tok, n)
		for k := 0; k < n; k++ {
			body[r][k] = tokensFromRuns(inlineRuns(rows[r][k]), mdPlain)
			widths[k] = maxInt(widths[k], tokensWidth(body[r][k]))
		}
	}

	// Shrink the widest columns until the table fits. The grid spends 3 cells per
	// internal column gap (" │ ") plus a 2-cell border on each side ("│ … │"), so
	// the columns share width - (3n + 1).
	avail := width - (3*n + 1)
	if avail < n {
		avail = n
	}
	total := 0
	for _, w := range widths {
		total += w
	}
	for total > avail {
		idx := -1
		for k, w := range widths {
			if w > 1 && (idx < 0 || w > widths[idx]) {
				idx = k
			}
		}
		if idx < 0 {
			break
		}
		widths[idx]--
		total--
	}

	out := []string{tableBorder(widths, "┌", "┬", "┐")}
	out = append(out, renderTableRow(hdr, widths, align)...)
	if len(body) == 0 {
		out = append(out, tableBorder(widths, "└", "┴", "┘"))
		return out
	}
	out = append(out, tableBorder(widths, "├", "┼", "┤"))
	for r := range body {
		if r > 0 {
			out = append(out, tableBorder(widths, "├", "┼", "┤"))
		}
		out = append(out, renderTableRow(body[r], widths, align)...)
	}
	out = append(out, tableBorder(widths, "└", "┴", "┘"))
	return out
}

// renderTableRow wraps each cell to its column width and stacks the physical
// lines (a row is as tall as its tallest wrapped cell).
func renderTableRow(cells [][]tok, widths []int, align []int) []string {
	wrapped := make([][][]tok, len(cells))
	height := 1
	for k := range cells {
		wrapped[k] = wrapTokens(cells[k], widths[k], widths[k])
		height = maxInt(height, len(wrapped[k]))
	}
	var out []string
	for li := 0; li < height; li++ {
		var b strings.Builder
		b.WriteString(dim("│ "))
		for k := range cells {
			if k > 0 {
				b.WriteString(dim(" │ "))
			}
			var line []tok
			if li < len(wrapped[k]) {
				line = wrapped[k][li]
			}
			b.WriteString(padCell(line, widths[k], alignAt(align, k)))
		}
		b.WriteString(dim(" │"))
		out = append(out, b.String())
	}
	return out
}

func alignAt(align []int, k int) int {
	if k < len(align) {
		return align[k]
	}
	return 0
}

// padCell renders one cell line styled and padded to width, honouring alignment.
func padCell(line []tok, width, align int) string {
	if w := tokensWidth(line); w > width {
		line = truncateTokens(line, width)
	}
	pad := width - tokensWidth(line)
	if pad < 0 {
		pad = 0
	}
	left, right := 0, pad
	switch align {
	case 1:
		left, right = pad/2, pad-pad/2
	case 2:
		left, right = pad, 0
	}
	return strings.Repeat(" ", left) + renderTokens(line) + strings.Repeat(" ", right)
}

// tableBorder draws one horizontal rule of the grid: the corner/joint runes
// (top ┌┬┐, middle ├┼┤, bottom └┴┘) joined by a ─ span over each column. A span is
// w+2 cells, matching the space padding either side of the column's content.
func tableBorder(widths []int, left, mid, right string) string {
	var b strings.Builder
	b.WriteString(left)
	for k, w := range widths {
		if k > 0 {
			b.WriteString(mid)
		}
		b.WriteString(strings.Repeat("─", w+2))
	}
	b.WriteString(right)
	return dim(b.String())
}

func tokensWidth(ts []tok) int {
	w := 0
	for _, t := range ts {
		w += runeCells(t.r)
	}
	return w
}

func truncateTokens(ts []tok, width int) []tok {
	out := make([]tok, 0, len(ts))
	w := 0
	for _, t := range ts {
		cw := runeCells(t.r)
		if w+cw > width {
			break
		}
		w += cw
		out = append(out, t)
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
