// A small markdown renderer for a worker's answer: enough of the block and inline
// syntax that a formatted reply reads as formatting in the terminal instead of
// raw markers. It is not CommonMark — it handles headings, emphasis, inline code,
// links, list items, block quotes, fenced code, GFM pipe tables and horizontal
// rules, which is what the reasoning workers actually emit.
//
// Wrapping happens on plain tokens and styling is applied afterward, so the
// column math counts cells, not the ANSI bytes the styles add. Each rune carries
// its style, so emphasis that spans a line break keeps working on both lines.
// Fenced code is handed to highlight.go's lexer; tables are laid out in the second
// half of this file.
package render

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/niq-run/niq/app/client/tui/term"
)

// mdStyle is an inline text style. It is an enum rather than a function so two
// runs can be compared (func values are not comparable in Go).
type mdStyle int

const (
	mdPlain mdStyle = iota
	mdBold
	mdItalic
	mdCode
	mdDim
	mdLink
	mdHeading
)

// plain is the identity style for text that carries no markup.
func plain(s string) string { return s }

// styleFn maps a markdown style to its ANSI wrapper.
func styleFn(st mdStyle) func(string) string {
	switch st {
	case mdBold:
		return term.Bold
	case mdItalic:
		return term.Italic
	case mdCode:
		return term.CodeText
	case mdDim:
		return term.Dim
	case mdLink:
		return term.Underline
	case mdHeading:
		return term.BrandBold
	default:
		return plain
	}
}

// run is a stretch of text in one style, produced by the inline parser.
type run struct {
	text string
	st   mdStyle
}

// tok is one styled rune, the unit the wrapper breaks.
type tok struct {
	r  rune
	st mdStyle
}

var (
	headingRe    = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	unorderedRe  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	orderedRe    = regexp.MustCompile(`^(\s*)(\d+[.)])\s+(.*)$`)
	blockquoteRe = regexp.MustCompile(`^\s*>\s?(.*)$`)
)

// RenderMarkdown turns a worker's markdown answer into styled, width-wrapped
// lines. width is the full column budget (the body is flush left).
func RenderMarkdown(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	lines := strings.Split(text, "\n")
	inFence := false
	var fence []string
	fenceLang := ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				out = append(out, renderCode(fence, fenceLang, width)...)
				fence = nil
				fenceLang = ""
				inFence = false
			} else {
				inFence = true
				fenceLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			}
			continue
		}
		if inFence {
			fence = append(fence, line)
			continue
		}
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		// A row of cells followed by a delimiter row is a table.
		if strings.Contains(line, "|") && i+1 < len(lines) && isDelimRow(lines[i+1]) {
			header := splitRow(line)
			align := alignOf(lines[i+1])
			var rows [][]string
			j := i + 2
			for ; j < len(lines); j++ {
				t := strings.TrimSpace(lines[j])
				if t == "" || !strings.Contains(t, "|") {
					break
				}
				rows = append(rows, splitRow(lines[j]))
			}
			out = append(out, renderTable(header, align, rows, width)...)
			i = j - 1
			continue
		}
		if isRule(trimmed) {
			out = append(out, term.Dim(strings.Repeat("─", width)))
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			out = append(out, layoutParagraph(m[2], width, mdHeading)...)
			continue
		}
		if m := unorderedRe.FindStringSubmatch(line); m != nil {
			out = append(out, layoutItem(m[2], indentOf(m[1])+"• ", width)...)
			continue
		}
		if m := orderedRe.FindStringSubmatch(line); m != nil {
			out = append(out, layoutItem(m[3], indentOf(m[1])+m[2]+" ", width)...)
			continue
		}
		if m := blockquoteRe.FindStringSubmatch(line); m != nil {
			out = append(out, layoutQuote(m[1], width)...)
			continue
		}
		out = append(out, layoutParagraph(line, width, mdPlain)...)
	}
	if inFence && len(fence) > 0 {
		out = append(out, renderCode(fence, fenceLang, width)...)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// layoutParagraph wraps one inline-parsed paragraph at width, flush left. base is
// the style plain text takes (mdHeading for headings, mdPlain otherwise).
func layoutParagraph(text string, width int, base mdStyle) []string {
	ts := tokensFromRuns(inlineRuns(text), base)
	if len(ts) == 0 {
		return []string{""}
	}
	wrapped := wrapTokens(ts, width, width)
	out := make([]string, len(wrapped))
	for i, ln := range wrapped {
		out[i] = renderTokens(ln)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// layoutItem renders a list item: a bullet (or "1. ") then the item text, with
// continuation lines aligned under the text. bullet includes any leading indent.
func layoutItem(content, bullet string, width int) []string {
	bw := term.DisplayWidth(bullet)
	first := width - bw
	if first < 4 {
		first = 4
	}
	ts := tokensFromRuns(inlineRuns(content), mdPlain)
	wrapped := wrapTokens(ts, first, first)
	out := make([]string, 0, len(wrapped))
	pad := strings.Repeat(" ", bw)
	for i, ln := range wrapped {
		if i == 0 {
			out = append(out, bullet+renderTokens(ln))
		} else {
			out = append(out, pad+renderTokens(ln))
		}
	}
	if len(out) == 0 {
		out = []string{bullet}
	}
	return out
}

// layoutQuote renders a block quote with a dim bar, wrapped inside it.
func layoutQuote(content string, width int) []string {
	ts := tokensFromRuns(inlineRuns(content), mdDim)
	wrapped := wrapTokens(ts, width-2, width-2)
	out := make([]string, 0, len(wrapped))
	for _, ln := range wrapped {
		out = append(out, term.Dim("│ ")+renderTokens(ln))
	}
	if len(out) == 0 {
		out = []string{term.Dim("│ ")}
	}
	return out
}

// renderCode renders fenced code lines under a dim bar, syntax-highlighted for
// langName when it is recognised, and painted on the code surface so a block
// reads as a distinct panel.
func renderCode(lines []string, langName string, width int) []string {
	hl := newHighlighter(langName)
	bg := term.BandCode.Bg()
	body := make([]string, 0, len(lines))
	for _, l := range lines {
		body = append(body, term.Dim("│ ")+hl.line(l))
	}
	if bg == "" {
		return body
	}
	padded := make([]string, 0, len(body)+2)
	padded = append(padded, "")
	padded = append(padded, body...)
	padded = append(padded, "")
	out := make([]string, len(padded))
	for i, ln := range padded {
		out[i] = term.BandLine(ln, width, 0, bg)
	}
	return out
}

// inlineRuns scans inline markdown into styled runs. It recognises `code`,
// **bold**, *italic* and [text](url); everything else is plain. Dangling markers
// are left as literal text. Underscore emphasis is deliberately not supported so
// identifiers like run_program are never mangled.
func inlineRuns(s string) []run {
	var runs []run
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			runs = append(runs, run{buf.String(), mdPlain})
			buf.Reset()
		}
	}
	i := 0
	for i < len(s) {
		if s[i] == '`' {
			if j := strings.IndexByte(s[i+1:], '`'); j >= 0 {
				flush()
				runs = append(runs, run{s[i+1 : i+1+j], mdCode})
				i += j + 2
				continue
			}
		}
		if strings.HasPrefix(s[i:], "**") {
			if j := strings.Index(s[i+2:], "**"); j >= 0 {
				flush()
				runs = append(runs, run{s[i+2 : i+2+j], mdBold})
				i += j + 4
				continue
			}
		}
		if s[i] == '*' {
			if j := strings.IndexByte(s[i+1:], '*'); j > 0 {
				flush()
				runs = append(runs, run{s[i+1 : i+1+j], mdItalic})
				i += j + 2
				continue
			}
		}
		if s[i] == '[' {
			if close := strings.IndexByte(s[i:], ']'); close >= 0 &&
				i+close+1 < len(s) && s[i+close+1] == '(' {
				if end := strings.IndexByte(s[i+close+2:], ')'); end >= 0 {
					flush()
					runs = append(runs, run{s[i+1 : i+close], mdLink})
					i += close + end + 3
					continue
				}
			}
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		buf.WriteRune(r)
		i += size
	}
	flush()
	return runs
}

// tokensFromRuns flattens runs to styled runes, giving plain runs the paragraph's
// base style.
func tokensFromRuns(runs []run, base mdStyle) []tok {
	var ts []tok
	for _, rn := range runs {
		st := rn.st
		if st == mdPlain {
			st = base
		}
		for _, r := range rn.text {
			ts = append(ts, tok{r: r, st: st})
		}
	}
	return ts
}

// wrapTokens breaks styled runes into lines: firstWidth for the first line,
// restWidth for the rest. It mirrors breakAt — break at a boundary space or a
// wide rune, else at the last space that fits, else hard-break.
func wrapTokens(ts []tok, firstWidth, restWidth int) [][]tok {
	if firstWidth < 1 {
		firstWidth = 1
	}
	if restWidth < 1 {
		restWidth = 1
	}
	var out [][]tok
	w := firstWidth
	for len(ts) > 0 {
		cells, end, lastSpace := 0, 0, -1
		for end < len(ts) {
			cw := term.RuneCells(ts[end].r)
			if cells+cw > w {
				break
			}
			cells += cw
			if ts[end].r == ' ' {
				lastSpace = end
			}
			end++
		}
		if end == len(ts) {
			out = append(out, trimTrailingSpace(ts))
			break
		}
		if end == 0 {
			end = 1
		}
		cut := end
		if next := ts[end].r; next != ' ' && !term.IsWide(next) && lastSpace > 0 {
			cut = lastSpace
		}
		out = append(out, trimTrailingSpace(ts[:cut]))
		rest := ts[cut:]
		for len(rest) > 0 && rest[0].r == ' ' {
			rest = rest[1:]
		}
		ts = rest
		w = restWidth
	}
	if len(out) == 0 {
		out = [][]tok{{}}
	}
	return out
}

func trimTrailingSpace(ts []tok) []tok {
	for len(ts) > 0 && ts[len(ts)-1].r == ' ' {
		ts = ts[:len(ts)-1]
	}
	return ts
}

// renderTokens writes a token line, grouping adjacent same-style runes so each
// style spans one ANSI sequence rather than one per rune.
func renderTokens(ts []tok) string {
	var b strings.Builder
	for i := 0; i < len(ts); {
		j := i
		for j < len(ts) && ts[j].st == ts[i].st {
			j++
		}
		var seg strings.Builder
		for k := i; k < j; k++ {
			seg.WriteRune(ts[k].r)
		}
		b.WriteString(styleFn(ts[i].st)(seg.String()))
		i = j
	}
	return b.String()
}

// isRule reports a horizontal rule: three or more of -, * or _ (spaces allowed).
func isRule(s string) bool {
	if len(s) < 3 {
		return false
	}
	c := s[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] != c && s[i] != ' ' {
			return false
		}
	}
	return true
}

// indentOf maps a list item's leading whitespace to two spaces per level.
func indentOf(ws string) string {
	n := 0
	for _, r := range ws {
		if r == '\t' {
			n += 2
		} else if r == ' ' {
			n++
		}
	}
	return strings.Repeat("  ", n/2)
}

// ── GFM pipe tables ──

// A table is drawn as a full grid — an outer border, a rule between every pair of
// rows, and a │ between columns — and laid out to the terminal width: columns are
// sized to their content, then shrunk — widest first — until the table fits. Cell
// text wraps inside its column, so a long cell grows its row in height rather than
// pushing the table past the screen. Cells keep inline markdown (bold, code,
// links).

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
		b.WriteString(term.Dim("│ "))
		for k := range cells {
			if k > 0 {
				b.WriteString(term.Dim(" │ "))
			}
			var line []tok
			if li < len(wrapped[k]) {
				line = wrapped[k][li]
			}
			b.WriteString(padCell(line, widths[k], alignAt(align, k)))
		}
		b.WriteString(term.Dim(" │"))
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
	return term.Dim(b.String())
}

func tokensWidth(ts []tok) int {
	w := 0
	for _, t := range ts {
		w += term.RuneCells(t.r)
	}
	return w
}

func truncateTokens(ts []tok, width int) []tok {
	out := make([]tok, 0, len(ts))
	w := 0
	for _, t := range ts {
		cw := term.RuneCells(t.r)
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
