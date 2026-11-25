// The dock: the pinned bottom area — the status/rule border, the composer rows,
// and the closing rule. Layout only; the composer itself lives in input.go.
package tui

import (
	"fmt"
	"strings"
)

// ── Rendering ──

func (c *chat) viewHeight() int {
	// The dock is a blank spacer, the status rule, the input rows, a divider rule
	// under the input, and the context/model line at the very bottom.
	n := c.inputN
	if n < 1 {
		n = 1
	}
	bottom := 4 + n // spacer + status rule + input rows + divider rule + context line
	v := c.height - bottom
	if v < 1 {
		v = 1
	}
	return v
}

// rebuild recomputes the transcript lines from the retained events and filter.
// It is the only place that walks the event list, so live events and filter
// changes cost one render rather than one per event.
func (c *chat) rebuild() {
	c.lines = renderTranscript(c.events, c.filter, c.width, c.liveThinking, c.liveThinkingWorker)
	c.linesDirty = false
	c.vp.resize(c.viewHeight())
	c.vp.setTotal(len(c.lines))
}

// render paints one full frame: the visible transcript window, then the dock — a
// status/rule border, the input on its surface, and a closing rule. A modal (the
// keymap or the target picker) is painted last, over that frame.
func (c *chat) render() {
	if c.scr == nil || c.width <= 0 || c.height <= 0 {
		return
	}
	if c.linesDirty {
		c.rebuild()
	}
	c.hintRow = -1
	c.spans = nil
	c.ctxRow = -1
	c.ctxModelX0, c.ctxModelX1 = -1, -1
	c.cardRect = rect{}
	inRows, inCurRow, inCurCol := c.inputRows()
	c.inputN = len(inRows)
	vh := c.viewHeight()
	c.vp.resize(vh)
	c.vp.setTotal(len(c.lines))
	start, _ := c.vp.window()

	rows := make([]string, c.height)
	for i := 0; i < vh && i < c.height; i++ {
		idx := start + i
		if idx >= len(c.lines) {
			break
		}
		line := c.lines[idx]
		if from, to, ok := c.sel.columns(idx, visibleWidth(line)); ok {
			line = selectRange(line, from, to)
		}
		rows[i] = line
	}

	// Dock, from the top: a blank spacer, the status/rule border, the input rows, a
	// divider rule under the input, and the context/model line at the very bottom
	// (below that rule). The dock has no surface of its own: the rules are what
	// separate it from the transcript.
	bottom := c.height - vh
	curRow, curCol := -1, 0
	idx := vh
	if bottom >= 3 && idx < c.height {
		rows[idx] = ""
		idx++
	}
	if idx < c.height {
		rows[idx] = c.hint()
		c.hintRow = idx
		idx++
	}
	for i, line := range inRows {
		if idx >= c.height {
			break
		}
		rows[idx] = line
		if i == inCurRow && c.menu == menuNone {
			curRow, curCol = idx, inCurCol
		}
		idx++
	}
	if idx < c.height {
		rows[idx] = c.rule()
		idx++
	}
	if idx < c.height {
		rows[idx] = c.contextLine()
		c.ctxRow = idx
		idx++
	}
	// The modals paint last, over the finished frame: they cover only their own
	// rows, so the transcript around them stays readable.
	switch {
	case c.help:
		c.paintHelp(rows)
		curRow = -1
	case c.menu != menuNone:
		c.paintMenu(rows)
		curRow = -1
	}
	c.scr.render(rows, curRow, curCol)
}

// hintAct is what a clickable segment of the status rule opens.
type hintAct int

const (
	hintNone hintAct = iota
	hintHelp
	hintTargetPick
	hintViewPick
	hintInputModePick
	hintVimPick
	hintModelPick
)

// hintSpan is a clickable segment of the status rule. x0/x1 are 0-based columns.
type hintSpan struct {
	x0, x1 int
	act    hintAct
}

// stateHint is one candidate for the rule's right side. vim reports whether it
// starts with the insert/normal word (the clickable vim state).
type stateHint struct {
	text string
	vim  bool
}

// hint is the status rule. Every segment is a click target: target, view, input
// mode and vim mode open their pickers, help opens the keymap. Shortcut reminders
// are deliberately absent — the help page carries them.
func (c *chat) hint() string {
	if c.width < 1 {
		return ""
	}
	// Keep the right side (state and help) and shrink the left before dropping any
	// of it: the help entry is the access point, so it outranks the scope labels.
	for _, right := range c.stateHints() {
		for _, level := range []int{2, 1, 0} {
			l, ls := c.hintLeft(level)
			if line, ok := c.hintLine(l, ls, right); ok {
				return c.focusLine(line)
			}
		}
	}
	// Nothing fits beside a right side; use the widest left that fits alone.
	for _, level := range []int{2, 1, 0} {
		l, ls := c.hintLeft(level)
		if 3+visibleWidth(l) < c.width {
			line, _ := c.hintLine(l, ls, stateHint{})
			return c.focusLine(line)
		}
	}
	line, _ := c.hintLine("", nil, stateHint{})
	return c.focusLine(line)
}

// focusLine paints the keyboard-focused button as a reverse-video block over the
// finished rule (a filled highlight rather than a text style), using the span the
// last hintLine recorded.
func (c *chat) focusLine(line string) string {
	if c.focus == hintNone || line == "" {
		return line
	}
	for _, s := range c.spans {
		if s.act != c.focus {
			continue
		}
		w := visibleWidth(line)
		return sliceStyled(line, 0, s.x0) + reverse(plainCells(line, s.x0, s.x1)) + sliceStyled(line, s.x1, w)
	}
	return line
}

// hintLeft builds the rule's left side: target, view scope and input mode. Level
// 2 has all three, 1 drops the mode, 0 keeps only the target. It also returns the
// clickable spans, relative to the left text.
func (c *chat) hintLeft(level int) (string, []hintSpan) {
	var b strings.Builder
	var spans []hintSpan
	add := func(act hintAct, s string) {
		x0 := visibleWidth(b.String())
		b.WriteString(s)
		spans = append(spans, hintSpan{x0: x0, x1: x0 + visibleWidth(s), act: act})
	}
	// The " · " separators are written between the buttons, outside their spans,
	// so a focused button's highlight never swallows the dot.
	sep := func() { b.WriteString(dim(" · ")) }

	add(hintTargetPick, dim("target: ")+brandBold(orStr(c.target, "?")))
	if level == 0 {
		return b.String(), spans
	}
	view := "all"
	if c.filter != "" {
		view = c.filter
	}
	sep()
	add(hintViewPick, dim("view: ")+brandBold(view))
	if level == 1 || c.in == nil {
		return b.String(), spans
	}
	sep()
	add(hintInputModePick, dim("mode: ")+brandBold(c.in.modeLabel()))
	return b.String(), spans
}

// hintLine draws the rule around one left side and one right side, recording the
// clickable spans of both. An empty right means "no right side".
func (c *chat) hintLine(l string, leftSpans []hintSpan, right stateHint) (string, bool) {
	lead := dim("── ")
	used := 3 + visibleWidth(l)
	c.spans = nil
	if used >= c.width {
		return clipStyled(lead+l, c.width), false
	}
	for _, s := range leftSpans {
		c.spans = append(c.spans, hintSpan{x0: s.x0 + 3, x1: s.x1 + 3, act: s.act})
	}
	if right.text == "" {
		fill := c.width - used - 3
		if fill < 0 {
			fill = 0
		}
		return lead + l + dim(" ") + dim(strings.Repeat("─", fill)) + dim("──"), false
	}
	rw := visibleWidth(right.text)
	if used+rw+6 > c.width {
		return "", false
	}
	fill := c.width - used - rw - 5
	rightStart := used + fill + 2
	end := rightStart + rw
	if right.vim {
		c.spans = append(c.spans, hintSpan{x0: rightStart, x1: rightStart + displayWidth("insert"), act: hintVimPick})
	}
	helpW := displayWidth("help")
	c.spans = append(c.spans, hintSpan{x0: end - helpW, x1: end, act: hintHelp})
	return lead + l + dim(" ") + dim(strings.Repeat("─", fill)) + dim(" ") + right.text + dim(" ──"), true
}

// hintAction reports which segment a click landed on.
func (c *chat) hintAction(row, col int) (hintAct, bool) {
	if row != c.hintRow {
		return hintNone, false
	}
	for _, s := range c.spans {
		if col >= s.x0 && col < s.x1 {
			return s.act, true
		}
	}
	return hintNone, false
}

// focusHints is the order Tab visits the buttons and the model, left to right.
var focusHints = []hintAct{hintTargetPick, hintViewPick, hintInputModePick, hintVimPick, hintHelp, hintModelPick}

// presentHints returns the focusable targets currently drawn, in focus order, so
// Tab skips the ones a narrow terminal dropped.
func (c *chat) presentHints() []hintAct {
	present := make(map[hintAct]bool, len(c.spans)+1)
	for _, s := range c.spans {
		present[s.act] = true
	}
	// The model lives on the context line under the rule, not on the rule itself,
	// so it counts as present whenever that line drew a model region.
	if c.ctxModelX1 > c.ctxModelX0 {
		present[hintModelPick] = true
	}
	out := make([]hintAct, 0, len(focusHints))
	for _, a := range focusHints {
		if present[a] {
			out = append(out, a)
		}
	}
	return out
}

// cycleFocus moves the keyboard focus to the next (dir > 0) or previous button,
// wrapping. With nothing focused yet it lands on the first (or last) button.
func (c *chat) cycleFocus(dir int) {
	acts := c.presentHints()
	if len(acts) == 0 {
		c.focus = hintNone
		return
	}
	idx := -1
	for i, a := range acts {
		if a == c.focus {
			idx = i
			break
		}
	}
	if idx < 0 {
		if dir > 0 {
			c.focus = acts[0]
		} else {
			c.focus = acts[len(acts)-1]
		}
		return
	}
	c.focus = acts[(idx+dir+len(acts))%len(acts)]
}

// contextWorker is the worker the dock's context/model line describes: the view's
// bound worker when scoped, else the worker of the newest response.
func (c *chat) contextWorker() string {
	if c.filter != "" {
		return c.filter
	}
	for i := len(c.events) - 1; i >= 0; i-- {
		if c.events[i].Type == "reason.response" {
			return c.events[i].WorkerID
		}
	}
	return ""
}

// usageFor is the newest response's context accounting for one worker.
func (c *chat) usageFor(worker string) (usageInfo, bool) {
	for i := len(c.events) - 1; i >= 0; i-- {
		e := c.events[i]
		if e.Type == "reason.response" && e.WorkerID == worker {
			return parseUsage(e.Payload)
		}
	}
	return usageInfo{}, false
}

// workerDisplay is a worker id's friendly name (its label when the bus reports
// one, else the id).
func (c *chat) workerDisplay(id string) string {
	for _, w := range c.partners {
		if w.ID == id {
			return workerName(w)
		}
	}
	return workerLabel(id)
}

// modelLabel is the current model of a worker, empty until its providers are
// known.
func (c *chat) modelLabel(worker string) string {
	if worker == "" {
		return ""
	}
	return c.providers[worker].Current.Model
}

// contextLine is the rule under the composer: on the left, the bound worker and
// its latest context occupancy; on the right, the worker's current model, which
// is the click target that switches it.
func (c *chat) contextLine() string {
	c.ctxModelX0, c.ctxModelX1 = -1, -1
	if c.width < 1 {
		return ""
	}
	worker := c.contextWorker()

	var left strings.Builder
	if worker == "" {
		left.WriteString(dim("ctx —"))
	} else {
		// The worker is identified by brackets, not colour: blue means "clickable",
		// and this label is not a target.
		left.WriteString(dim("ctx "))
		left.WriteString("[" + c.workerDisplay(worker) + "]")
		if u, ok := c.usageFor(worker); ok {
			left.WriteString(dim(" " + u.percent()))
		}
	}

	var right, plainRight string
	if model := c.modelLabel(worker); model != "" {
		plainRight = "model " + model + " ▾"
		right = dim("model ") + brandBold(model) + dim(" ▾")
	} else {
		plainRight = "model —"
		right = dim("model —")
	}
	// The model is both a click target and a Tab stop; focused, it shows as a
	// filled block like the rule's buttons.
	if c.focus == hintModelPick {
		right = reverse(plainRight)
	}

	ls := left.String()
	lw, rw := visibleWidth(ls), visibleWidth(right)
	if lw+rw+1 > c.width {
		// No room for both: keep the occupancy and drop the model, or, failing
		// that, clip the left side.
		if rw+1 <= c.width {
			ls = clipStyled(ls, c.width-rw-1)
			lw = visibleWidth(ls)
		} else {
			return clipStyled(ls, c.width)
		}
	}
	fill := c.width - lw - rw
	if fill < 1 {
		fill = 1
	}
	c.ctxModelX0 = c.width - rw
	c.ctxModelX1 = c.width
	return ls + strings.Repeat(" ", fill) + right
}

// stateHints is the rule's right side, most detailed first: the vim state, then
// any status, then the help entry. It always ends with the help entry.
func (c *chat) stateHints() []stateHint {
	mode := brandBold("insert")
	if c.normal {
		mode = brandBold("normal")
	}
	help := brand("help")
	bits := c.statusBits()
	out := make([]stateHint, 0, len(bits)+2)
	for n := len(bits); n >= 0; n-- {
		var b strings.Builder
		b.WriteString(mode)
		for _, bit := range bits[:n] {
			b.WriteString(dim(" · " + bit))
		}
		b.WriteString(dim(" · ") + help)
		out = append(out, stateHint{text: b.String(), vim: true})
	}
	return append(out, stateHint{text: help})
}

// statusBits are the transient facts printed beside the state: attachments, the
// scroll position, loading and the last status message.
func (c *chat) statusBits() []string {
	var bits []string
	if c.in != nil && len(c.in.attach) > 0 {
		b := fmt.Sprintf("%d attached", len(c.in.attach))
		if c.uploadFiles {
			b += " (upload)"
		}
		bits = append(bits, b)
	}
	if !c.vp.follow {
		above, below := c.vp.offset()
		bits = append(bits, fmt.Sprintf("↑%d ↓%d", above, below))
	} else if c.loading {
		bits = append(bits, "loading…")
	}
	if c.status != "" {
		bits = append(bits, c.status)
	}
	return bits
}

// rule is a full-width dim horizontal rule.
func (c *chat) rule() string {
	w := c.width
	if w < 1 {
		w = 1
	}
	return dim(strings.Repeat("─", w))
}

// inputRows is the dock's composer rows. The pickers are popups drawn over the
// frame, not dock rows, so this stays the input's own layout.
func (c *chat) inputRows() (rows []string, curRow, curCol int) {
	if c.in == nil {
		return []string{brand("› ")}, 0, 2
	}
	return c.in.rows(c.width)
}
