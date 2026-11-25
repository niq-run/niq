// Conversation rendering for the terminal: how one bus event becomes a block of
// text, and how that text is laid out. Everything here is a pure function over an
// event, so the layout rules can be tested without a TTY.
//
// Two shapes only:
//
//   - a conversation block (the human's input, a worker's response) is the
//     message itself: the speaker's prefix on the first line, and every wrapped
//     line flush left at column 0 so the body never drifts right by the width of
//     a long speaker name;
//   - an event line (thinking, a tool call, a request result) is one line of
//     noise-reduced context: a bold bracketed label naming the event, then a
//     dim, width-capped detail. It never wraps — overflow becomes an ellipsis —
//     so a long thinking trace cannot push the conversation off screen.
package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/niq-run/niq/app/client/tui/text"
	"github.com/niq-run/niq/app/standalone/contract"
)

// rendered is one block ready to print. plainPrefix is the unstyled prefix (its
// width is what the first line wraps around); prefix is its styled twin.
// chat marks a conversation block, which the view sets off with blank lines
// around it. inline keeps a short message (the human's) on the prefix line;
// markdown renders the body (a worker's answer) through the markup renderer.
type rendered struct {
	plainPrefix string
	prefix      string
	// header is a pre-styled opening line (a response's speaker rule). When set it
	// replaces the prefix line.
	header string
	// inset is how many cells the whole block is indented by, so an unbanded block
	// lines its text up with the text inside the banded cards beside it.
	inset     int
	text      string
	textStyle func(string) string
	chat      bool
	inline    bool
	markdown  bool
	// body is a fully laid-out, styled block (a tool card). When set it is used
	// verbatim instead of the prefix/text shape.
	body []string
	// footer is a dim trailing line under a chat body — a response's context-usage
	// label. Rendered at the block's inset.
	footer string
	// band is the background surface the block is painted on; bandNone keeps the
	// terminal's own background.
	band bandKind
}

// evKind is the colour family of an event row's bracketed title.
type evKind int

const (
	evOther evKind = iota
	evThinking
	evWarn
	evDanger
)

// label colours the event title for its kind. Thinking is grey (secondary), tool
// rows plain bold (so they read on the green tool surface), warnings bright
// yellow on their amber surface.
func (k evKind) label(s string) string {
	switch k {
	case evThinking:
		return grayBold(s)
	case evWarn:
		return yellowBright(s)
	case evDanger:
		return redBold(s)
	default:
		return bold(s)
	}
}

// body is the detail style for the kind.
func (k evKind) body() func(string) string {
	switch k {
	case evThinking:
		return gray
	case evWarn:
		return warnText
	case evDanger:
		return red
	}
	return dim
}

// plain is the identity style: conversation text is left uncoloured so the
// styled prefixes stand out.
func plain(s string) string { return s }

// noisyEvent reports the events a conversation view drops: delivery markers,
// worker lifecycle presence, and live-only partials (streaming deltas, tool
// progress). Everything else is shown, so the terminal stays a readable event
// stream rather than a filtered one.
func noisyEvent(ev contract.Event) bool {
	if ev.Transient {
		return true
	}
	switch ev.Type {
	case "event.delivered", "worker.ready", "worker.gone", "worker.discover",
		"reason.start", "reason.end",
		"reason.thinking_delta", "reason.text_delta", "request.progressed":
		return true
	}
	return false
}

// describe turns one event into a block. Its second result is false for the
// events noisyEvent drops, so the caller can skip them entirely.
func describe(ev contract.Event, width int) (rendered, bool) {
	if noisyEvent(ev) {
		return rendered{}, false
	}
	switch ev.Type {
	case "worker.input":
		// The human's own message, sent through this or any other interface. It
		// stays on the prefix line (a short message reads better inline), is
		// underlined, and is the one block set off by a blank line above it. When
		// the bus recorded whom it was addressed to, that is shown after the
		// speaker — `you → niq › …` — so every message says where it went.
		plainPrefix := "you › "
		prefix := greenBold("you") + dim(" › ")
		if to := inputRecipients(ev); len(to) > 0 {
			joined := strings.Join(to, ", ")
			plainPrefix = "you → " + joined + " › "
			prefix = greenBold("you") + dim(" → ") + brandBold(joined) + dim(" › ")
		}
		return rendered{
			plainPrefix: plainPrefix,
			prefix:      prefix,
			text:        text.StripSystemReminder(text.ContentText(ev.Payload["content"], ev.Payload["text"])),
			textStyle:   plain,
			chat:        true,
			inline:      true,
			band:        bandUser,
		}, true
	case "reason.response":
		// The answer: a speaker rule opens the section at the left edge, a blank row
		// sets the body off from it, and the body follows as rendered markdown, inset
		// to match the banded cards so prose and their text share a left edge. The
		// round's context occupancy trails it as a dim footer, like the WebUI's.
		label := workerLabel(ev.WorkerID)
		r := rendered{
			header:    sectionRule(label, width),
			inset:     replyInset,
			text:      text.ContentText(ev.Payload["content"], nil),
			textStyle: plain,
			chat:      true,
			markdown:  true,
		}
		if u, ok := parseUsage(ev.Payload); ok {
			r.footer = u.footer()
		}
		return r, true
	case "reason.thinking":
		return thinkingBlock(text.ContentText(ev.Payload["content"], nil), width), true
	case "reason.interrupted":
		return eventLine("interrupted", text.StringField(ev.Payload, "reason"), evWarn, width), true
	case "worker.abort":
		return eventLine("abort", "", evWarn, width), true
	case "timer.timeout":
		return eventLine("timeout", timeoutDetail(ev), evWarn, width), true
	case "request.cancel":
		return eventLine("cancel", cancelDetail(ev), evWarn, width), true
	case "approval.request":
		return eventLine("approval", text.StringField(ev.Payload, "action"), evOther, width), true
	}
	// Everything else is a tool invocation or a request result: a single labelled
	// line so the stream reads as an event log without flooding the conversation.
	kind := evOther
	if ev.Type == "request.failed" || ev.Type == "request.rejected" {
		kind = evDanger
	}
	return eventLine(ev.Type, text.JSONOneLine(ev.Payload), kind, width), true
}

// eventLine is one dim, single-line event row: a coloured label naming what
// happened, a colon, then the detail capped to the remaining width. The human and
// worker messages keep their `›` arrow; event rows use the plainer `label:` form.
// A long detail (a thinking trace, a large payload) is truncated with an ellipsis
// rather than wrapped, so one event can never occupy more than one line.
func eventLine(label, detail string, kind evKind, width int) rendered {
	label = strings.TrimSpace(label)
	band := bandTool
	switch kind {
	case evDanger:
		band = bandError
	case evWarn:
		band = bandWarn
	case evThinking:
		band = bandNone
	}
	if label == "" {
		return rendered{plainPrefix: "· ", prefix: dim("· "), text: fitOneLine(detail, width-2), textStyle: dim, band: band}
	}
	sep := ": "
	if detail == "" {
		sep = ""
	}
	plainPrefix := label + sep
	return rendered{
		plainPrefix: plainPrefix,
		prefix:      kind.label(label) + dim(sep),
		text:        fitOneLine(detail, width-displayWidth(plainPrefix)-1),
		textStyle:   kind.body(),
		band:        band,
	}
}

// timeoutDetail reads a fired timer's structured result — a JSON string in the
// payload — and names the timer by its purpose, falling back to the duration it
// was armed with when no purpose was given. It returns "" when the result is
// missing or unreadable, leaving the row as the bare label.
func timeoutDetail(ev contract.Event) string {
	raw := text.StringField(ev.Payload, "result")
	if raw == "" {
		return ""
	}
	var res struct {
		Purpose    string  `json:"purpose"`
		DurationMs float64 `json:"duration_ms"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return ""
	}
	if p := strings.TrimSpace(res.Purpose); p != "" {
		return p
	}
	if res.DurationMs > 0 {
		return (time.Duration(res.DurationMs) * time.Millisecond).String()
	}
	return ""
}

// cancelDetail names what a request.cancel addressed: the timer being cancelled
// and, when the bus recorded a target, the worker it went to. Either part may be
// absent, so the row degrades to just the timer id or the target.
func cancelDetail(ev contract.Event) string {
	timer := text.StringField(ev.Payload, "timer_id")
	target := toolTarget(ev)
	switch {
	case target != "" && timer != "":
		return target + " · " + timer
	case timer != "":
		return timer
	default:
		return target
	}
}

// terminalAnswer reports whether a request type is the final answer to a call.
// request.cancel / request.progressed are lifecycle noise, not answers.
func terminalAnswer(t string) bool {
	switch t {
	case "request.completed", "request.failed", "request.rejected":
		return true
	}
	return false
}

// toolLike reports whether an event renders as a tool card: a domain event the
// reason worker published (its type names the capability, its payload is the
// arguments) or anything else that is neither a message nor a lifecycle row.
// The dedicated types keep their own shapes, and answers pair with their calls.
func toolLike(ev contract.Event) bool {
	if noisyEvent(ev) || terminalAnswer(ev.Type) {
		return false
	}
	switch ev.Type {
	case "worker.input", "worker.abort", "timer.timeout", "request.cancel",
		"reason.interrupted", "reason.thinking", "reason.response",
		"approval.request":
		return false
	}
	return true
}

// resultText is an answer's reply as text: the error or rejection reason, the
// `result` wrapper, or — for replies that carry no wrapper — the payload itself
// (the bus rule for object replies).
func resultText(ev contract.Event) string {
	switch ev.Type {
	case "request.failed":
		if s := text.StringField(ev.Payload, "error"); s != "" {
			return s
		}
	case "request.rejected":
		if s := text.StringField(ev.Payload, "reason"); s != "" {
			return s
		}
	}
	if s, ok := ev.Payload["result"].(string); ok {
		return s
	}
	if v, ok := ev.Payload["result"]; ok && v != nil {
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	if content, ok := ev.Payload["content"]; ok {
		if s := text.PayloadText(content); s != "" {
			return s
		}
	}
	return text.JSONOneLine(ev.Payload)
}

// toolResultLines caps how many reply rows a card shows, after the call line.
const toolResultLines = 2

// toolBlock is one call as a card, in pi's shape: the call on the first line —
// the tool name, the worker it addressed and the argument payload, truncated
// with an ellipsis — then a blank spacer, then up to two rows of the reply in a
// softer tone. A failed or rejected reply paints the card on the error surface.
func toolBlock(ev contract.Event, ans *contract.Event, width int) rendered {
	inner := width - 2 // one column of surface inset each side
	if inner < 8 {
		inner = 8
	}

	head := toolCall("$ ") + toolCall(ev.Type)
	plainHead := "$ " + ev.Type
	if to := toolTarget(ev); to != "" {
		head += toolOut(" → ") + toolCall(to)
		plainHead += " → " + to
	}
	if args := text.JSONOneLine(ev.Payload); args != "" {
		budget := inner - displayWidth(plainHead) - 2
		if budget < 4 {
			budget = 4
		}
		head += toolOut("  " + fitOneLine(args, budget))
	}
	body := []string{clipStyled(head, inner)}

	failed := ans != nil && ans.Type != "request.completed"
	if ans != nil {
		if lines := wrapMax(resultText(*ans), inner, toolResultLines); len(lines) > 0 {
			body = append(body, "") // spacer between the call and its reply
			for _, ln := range lines {
				if failed {
					body = append(body, toolFail(ln))
				} else {
					body = append(body, toolOut(ln))
				}
			}
		}
	}

	band := bandTool
	if failed {
		band = bandError
	}
	return rendered{body: body, band: band}
}

// replyInset is the worker's-answer indent: one cell of left margin, matching the
// inset the banded cards use, so prose lines up with the text inside them.
const replyInset = 1

// thinkingBlock renders a reasoning trace: grey, no surface, at most
// maxThinkingRows content lines. A longer trace keeps its first three lines and
// its newest seven and says how many it hid in between, so a stream that grows
// without bound stays scannable while the latest reasoning is always visible.
//
// The head and tail are cut on the trace's own line breaks, not on the wrapped
// rows, so the omission never lands in the middle of a sentence; a trace with no
// line breaks at all falls back to cutting its wrapped rows.
func thinkingBlock(text string, width int) rendered {
	const label = "thinking: "
	budget := bandInner(width) - displayWidth(label)
	src := splitLines(text)
	if len(src) == 0 {
		return rendered{body: []string{grayBold("thinking")}, band: bandNone}
	}

	omitted := 0
	var rows []string
	if len(src) > maxThinkingRows {
		omitted = len(src) - maxThinkingRows
		rows = wrapAll(strings.Join(src[:thinkingHead], "\n"), budget)
		rows = append(rows, thinkingMarker)
		rows = append(rows, wrapAll(strings.Join(src[len(src)-thinkingTail:], "\n"), budget)...)
	} else {
		rows = wrapAll(text, budget)
		if len(rows) > maxThinkingRows {
			omitted = len(rows) - maxThinkingRows
			kept := append([]string{}, rows[:thinkingHead]...)
			kept = append(kept, thinkingMarker)
			kept = append(kept, rows[len(rows)-thinkingTail:]...)
			rows = kept
		}
	}

	lines := make([]string, 0, len(rows))
	for i, row := range rows {
		switch {
		case row == thinkingMarker:
			lines = append(lines, " "+gray(fmt.Sprintf("··· %d lines omitted ···", omitted)))
		case i == 0:
			lines = append(lines, " "+grayBold(label)+gray(row))
		case row == "":
			lines = append(lines, "")
		default:
			lines = append(lines, " "+gray(row))
		}
	}
	return rendered{body: lines, band: bandNone}
}

// thinkingMarker stands in for the omission row while the block is being laid out.
const thinkingMarker = "\x00thinking-marker"

// thinking display limits: ten content rows, keeping the head and the newest tail
// with an omission marker between them.
const (
	maxThinkingRows = 10
	thinkingHead    = 3
	thinkingTail    = 7
)
