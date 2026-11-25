// Layout: how one block's text becomes terminal lines — wrapping, the inline
// (human message) shape, the answer shape with its inset, and the cell helpers
// the wrapping is built on.
package tui

import (
	"strings"
)

// fitOneLine collapses any whitespace run (including newlines) to a single space
// and truncates the result to at most max cells, ending an overlong string with
// an ellipsis. It is what keeps every event row to exactly one line.
func fitOneLine(text string, max int) string {
	text = strings.Join(strings.Fields(text), " ")
	if max < 1 {
		max = 1
	}
	if displayWidth(text) <= max {
		return text
	}
	if max == 1 {
		return "…"
	}
	return truncateCells(text, max-1) + "…"
}

// bandInner is the width available inside a banded block: one column of inset on
// each side, which is what renderBlock applies.
const bandInset = 1

func bandInner(width int) int {
	inner := width - 2*bandInset
	if inner < 8 {
		inner = 8
	}
	return inner
}

// blockBody lays one block out as terminal lines, unbanded. A non-chat row is a
// single prefixed line, and a tool card is returned verbatim. A conversation block
// either stays inline on the prefix line (the human's short message) or opens with
// its header/prefix and starts the body below — plain text, or rendered markdown
// for a worker's answer.
func blockBody(r rendered, width int) []string {
	if !r.chat {
		if r.body != nil {
			return r.body
		}
		text := fitOneLine(r.text, width-displayWidth(r.plainPrefix)-1)
		return []string{r.prefix + r.textStyle(text)}
	}
	if r.inline {
		// The band insets the card by one column on each side, so the text wraps to
		// that inner width: wrapping at the full width and then clipping to the inner
		// one silently cut the tail of every long line.
		return inlineLines(r, bandInner(width))
	}
	// An unbanded block is indented so its text shares a left edge with the text
	// inside the banded cards. The header is already built to that width.
	inset := r.inset
	pad := strings.Repeat(" ", inset)
	inner := width - 2*inset
	if inner < 8 {
		inner = 8
	}
	var lines []string
	switch {
	case r.header != "":
		// The rule is built at full width and stays at the left edge; only the body
		// below it takes the inset.
		lines = []string{r.header, ""}
	case r.prefix != "":
		lines = []string{pad + r.prefix}
	}
	// addFooter appends the dim trailing line (a response's context usage) at the
	// block's inset, if it has one.
	addFooter := func(ls []string) []string {
		if r.footer == "" {
			return ls
		}
		return append(ls, pad+dim(r.footer))
	}
	if r.text == "" {
		return addFooter(lines)
	}
	if r.markdown {
		for _, ln := range renderMarkdown(r.text, inner) {
			lines = append(lines, pad+ln)
		}
		return addFooter(lines)
	}
	budget := inner
	if budget < 8 {
		budget = 8
	}
	for _, para := range strings.Split(r.text, "\n") {
		rs := []rune(para)
		if len(rs) == 0 {
			lines = append(lines, "")
			continue
		}
		for len(rs) > 0 {
			cut := breakAt(rs, budget)
			line := strings.TrimRight(string(rs[:cut]), " ")
			lines = append(lines, pad+r.textStyle(line))
			rs = trimLeadingSpaces(rs[cut:])
		}
	}
	return addFooter(lines)
}

// inlineLines keeps a message on its prefix line and wraps continuation lines
// flush left. It is the human's own short input: `you › hello`.
func inlineLines(r rendered, width int) []string {
	firstBudget := width - displayWidth(r.plainPrefix)
	if firstBudget < 8 {
		firstBudget = 8
	}
	restBudget := width
	if restBudget < 8 {
		restBudget = 8
	}
	var lines []string
	first := true
	for _, para := range strings.Split(r.text, "\n") {
		rs := []rune(para)
		if len(rs) == 0 {
			if first {
				lines = append(lines, r.prefix)
				first = false
			} else {
				lines = append(lines, "")
			}
			continue
		}
		for len(rs) > 0 {
			budget := restBudget
			if first {
				budget = firstBudget
			}
			cut := breakAt(rs, budget)
			line := strings.TrimRight(string(rs[:cut]), " ")
			if first {
				lines = append(lines, r.prefix+r.textStyle(line))
				first = false
			} else {
				lines = append(lines, r.textStyle(line))
			}
			rs = trimLeadingSpaces(rs[cut:])
		}
	}
	if len(lines) == 0 {
		lines = []string{r.prefix}
	}
	return lines
}

// breakAt returns how many runes of rs fit in width cells. It breaks per
// character at a wide (CJK/emoji) rune or at the space sitting on the boundary,
// and otherwise prefers the last space that fits so an ASCII word stays whole. A
// token longer than the line is hard-broken.
func breakAt(rs []rune, width int) int {
	cells, end, lastSpace := 0, 0, -1
	for end < len(rs) {
		w := runeCells(rs[end])
		if cells+w > width {
			break
		}
		cells += w
		if rs[end] == ' ' {
			lastSpace = end
		}
		end++
	}
	if end == len(rs) {
		return end
	}
	if end == 0 {
		return 1 // a rune wider than the line: take it so the loop progresses
	}
	// A wide rune at the boundary is a natural character break; a space there is
	// simply dropped. Only an ASCII rune wants the word-aware fallback.
	if next := rs[end]; next == ' ' || isWide(next) {
		return end
	}
	if lastSpace > 0 {
		return lastSpace
	}
	return end
}

// trimLeadingSpaces drops the spaces a wrapped line left at the start of the next.
func trimLeadingSpaces(rs []rune) []rune {
	for len(rs) > 0 && rs[0] == ' ' {
		rs = rs[1:]
	}
	return rs
}

// splitLines splits text into its non-empty-bounded lines, dropping the blank
// rows at the end (a stream usually ends mid-line).
func splitLines(text string) []string {
	raw := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(raw))
	for _, ln := range raw {
		out = append(out, strings.TrimRight(ln, " \t"))
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return out
}

// wrapAll hard-wraps text to width, honouring the newlines already in it and
// dropping trailing blank rows. Unlike wrapMax it keeps everything: the caller
// decides what to hide.
func wrapAll(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		rs := []rune(strings.TrimRight(para, " \t"))
		if len(rs) == 0 {
			out = append(out, "")
			continue
		}
		for len(rs) > 0 {
			cut := breakAt(rs, width)
			out = append(out, strings.TrimRight(string(rs[:cut]), " "))
			rs = trimLeadingSpaces(rs[cut:])
		}
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// wrapMax wraps text to width and keeps at most maxLines rows, ellipsizing the
// last when there is more. Blank paragraphs are dropped so the row budget shows
// content rather than air.
func wrapMax(text string, width, maxLines int) []string {
	if maxLines < 1 || text == "" {
		return nil
	}
	if width < 4 {
		width = 4
	}
	var out []string
	truncated := false
	for _, para := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		rs := []rune(strings.TrimRight(para, " \t"))
		if len(rs) == 0 {
			continue
		}
		for len(rs) > 0 {
			if len(out) == maxLines {
				truncated = true
				break
			}
			cut := breakAt(rs, width)
			out = append(out, strings.TrimRight(string(rs[:cut]), " "))
			rs = trimLeadingSpaces(rs[cut:])
		}
		if truncated {
			break
		}
	}
	if truncated && len(out) > 0 {
		last := out[len(out)-1]
		if width > 1 {
			last = truncateCells(last, width-1)
		}
		out[len(out)-1] = last + "…"
	}
	return out
}

// sectionRule opens a speaker's block with a rule carrying the name flush left:
// `niq ·········`. The line is a run of dots, which reads as a section break
// rather than a border, and dim so it does not compete with the answer. It is
// built at the full width and not indented, so it starts at the left edge; the
// body below keeps its one-cell inset.
func sectionRule(name string, width int) string {
	if width <= 0 {
		return ""
	}
	if width < 4 {
		return brandBold(truncateCells(name, width))
	}
	name = truncateCells(name, width-2)
	fill := width - displayWidth(name) - 1
	if fill < 1 {
		fill = 1
	}
	return brandBold(name) + dim(" "+dotRule(fill))
}

// dotRule is fill cells of dots. One dot per cell, so the rule reads as a light
// dotted line at any width.
func dotRule(fill int) string {
	return strings.Repeat("·", fill)
}
