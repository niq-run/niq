// A small markdown renderer for a worker's answer: enough of the block and inline
// syntax that a formatted reply reads as formatting in the terminal instead of
// raw markers. It is not CommonMark — it handles headings, emphasis, inline code,
// links, list items, block quotes, fenced code and horizontal rules, which is what
// the reasoning workers actually emit.
//
// Wrapping happens on plain tokens and styling is applied afterward, so the
// column math counts cells, not the ANSI bytes the styles add. Each rune carries
// its style, so emphasis that spans a line break keeps working on both lines.
package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"
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

// styleFn maps a markdown style to its ANSI wrapper.
func styleFn(st mdStyle) func(string) string {
	switch st {
	case mdBold:
		return bold
	case mdItalic:
		return italic
	case mdCode:
		return codeText
	case mdDim:
		return dim
	case mdLink:
		return underline
	case mdHeading:
		return brandBold
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

// renderMarkdown turns a worker's markdown answer into styled, width-wrapped
// lines. width is the full column budget (the body is flush left).
func renderMarkdown(text string, width int) []string {
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
				out = append(out, renderCode(fence, fenceLang)...)
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
			out = append(out, dim(strings.Repeat("─", width)))
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
		out = append(out, renderCode(fence, fenceLang)...)
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
	bw := displayWidth(bullet)
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
		out = append(out, dim("│ ")+renderTokens(ln))
	}
	if len(out) == 0 {
		out = []string{dim("│ ")}
	}
	return out
}

// renderCode renders fenced code lines under a dim bar, syntax-highlighted for
// langName when it is recognised, otherwise left plain.
func renderCode(lines []string, langName string) []string {
	hl := newHighlighter(langName)
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, dim("│ ")+hl.line(l))
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
			cw := runeCells(ts[end].r)
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
		if next := ts[end].r; next != ' ' && !isWide(next) && lastSpace > 0 {
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
