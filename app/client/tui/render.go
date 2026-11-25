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
	"strings"

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
	text        string
	textStyle   func(string) string
	chat        bool
	inline      bool
	markdown    bool
	// separate asks the view to put a blank line above this block. The human's
	// turns get one; a worker's answer follows its events directly, so the reply
	// is not pushed away from the thinking that produced it.
	separate bool
}

// evKind is the colour family of an event row's bracketed title.
type evKind int

const (
	evOther evKind = iota
	evThinking
	evWarn
	evDanger
)

// label colours the event title for its kind.
func (k evKind) label(s string) string {
	switch k {
	case evThinking:
		return magentaBold(s)
	case evWarn:
		return yellowBold(s)
	case evDanger:
		return redBold(s)
	default:
		return cyanBold(s)
	}
}

// body is the detail style for the kind.
func (k evKind) body() func(string) string {
	if k == evDanger {
		return red
	}
	return dim
}

// plain is the identity style: conversation text is left uncoloured so the
// styled prefixes stand out.
func plain(s string) string { return s }

// talkPartnerTypes are the worker types a terminal can converse with — the ones
// that consume worker.input and answer in the conversation. This mirrors the
// WebUI's TALK_PARTNER_TYPES so both interfaces agree on who is addressable.
var talkPartnerTypes = map[string]bool{"reason": true, "coding": true, "niq": true}

// talkPartners keeps the workers a message can be addressed to, in listing order.
func talkPartners(workers []contract.Worker) []contract.Worker {
	out := make([]contract.Worker, 0, len(workers))
	for _, w := range workers {
		if talkPartnerTypes[w.Type] {
			out = append(out, w)
		}
	}
	return out
}

// pickTarget chooses the worker a message goes to by default: the first online
// partner, falling back to the first partner when every one of them is offline
// (a send then surfaces the error). The user can change it at any time with the
// target selector.
func pickTarget(partners []contract.Worker) string {
	for _, w := range partners {
		if w.Online {
			return w.ID
		}
	}
	if len(partners) > 0 {
		return partners[0].ID
	}
	return ""
}

// workerName is a partner's display name: its label when set, else its id.
func workerName(w contract.Worker) string {
	if w.Label != "" {
		return w.Label
	}
	return w.ID
}

// indexOfWorker returns the index of id in ws, or 0 when it is not listed.
func indexOfWorker(ws []contract.Worker, id string) int {
	for i, w := range ws {
		if w.ID == id {
			return i
		}
	}
	return 0
}

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
			text:        contentText(ev.Payload["content"], ev.Payload["text"]),
			textStyle:   underline,
			chat:        true,
			inline:      true,
			separate:    true,
		}, true
	case "reason.response":
		// The answer: speaker on its own line, body below as rendered markdown,
		// following the events directly (no blank line above it).
		label := workerLabel(ev.WorkerID)
		return rendered{
			plainPrefix: label + " › ",
			prefix:      brandBold(label) + dim(" › "),
			text:        contentText(ev.Payload["content"], nil),
			textStyle:   plain,
			chat:        true,
			markdown:    true,
		}, true
	case "reason.thinking":
		return eventLine("thinking", contentText(ev.Payload["content"], nil), evThinking, width), true
	case "reason.interrupted":
		return eventLine("interrupted", stringField(ev.Payload, "reason"), evWarn, width), true
	case "worker.abort":
		return eventLine("abort", "", evWarn, width), true
	case "timer.timeout":
		return eventLine("timeout", "", evWarn, width), true
	case "request.cancel":
		return eventLine("cancel", "", evWarn, width), true
	case "approval.request":
		return eventLine("approval", stringField(ev.Payload, "action"), evOther, width), true
	}
	// Everything else is a tool invocation or a request result: a single labelled
	// line so the stream reads as an event log without flooding the conversation.
	kind := evOther
	if ev.Type == "request.failed" || ev.Type == "request.rejected" {
		kind = evDanger
	}
	return eventLine(ev.Type, jsonOneLine(ev.Payload), kind, width), true
}

// eventLine is one dim, single-line event row: a coloured label naming what
// happened, a colon, then the detail capped to the remaining width. The human and
// worker messages keep their `›` arrow; event rows use the plainer `label:` form.
// A long detail (a thinking trace, a large payload) is truncated with an ellipsis
// rather than wrapped, so one event can never occupy more than one line.
func eventLine(label, detail string, kind evKind, width int) rendered {
	label = strings.TrimSpace(label)
	if label == "" {
		return rendered{plainPrefix: "· ", prefix: dim("· "), text: fitOneLine(detail, width-2), textStyle: dim}
	}
	plainPrefix := label + ": "
	return rendered{
		plainPrefix: plainPrefix,
		prefix:      kind.label(label) + dim(": "),
		text:        fitOneLine(detail, width-displayWidth(plainPrefix)-1),
		textStyle:   kind.body(),
	}
}

// note renders a bare status line of the view's own (a stream note, a send
// failure). It is not a bus event, so it keeps the plain bullet rather than a
// bracketed label.
func note(text string) rendered {
	return rendered{plainPrefix: "· ", prefix: dim("· "), text: text, textStyle: dim}
}

// inputRecipients is whom the bus recorded a worker.input as addressed to: the
// routed Recipients slice, or the single-target field on older events.
func inputRecipients(ev contract.Event) []string {
	if len(ev.Recipients) > 0 {
		return ev.Recipients
	}
	if ev.TargetWorkerID != "" {
		return []string{ev.TargetWorkerID}
	}
	return nil
}

// workerLabel is the speaker name for the left side of a conversation block.
func workerLabel(id string) string {
	if id == "" {
		return "worker"
	}
	return id
}

// contentText reads the text out of an event's payload: a structured "content"
// array (llm-like blocks, or the string array reason.response carries) when
// present, otherwise the legacy "text" field.
func contentText(content any, legacyText any) string {
	if s := payloadText(content); s != "" {
		return s
	}
	if s, ok := legacyText.(string); ok {
		return s
	}
	return ""
}

// payloadText flattens a "content" array into display text. Text blocks (and
// bare strings) contribute their text; media blocks contribute a short marker so
// the reader knows something is attached without dumping base64 into a terminal.
func payloadText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		parts := make([]string, 0, len(c))
		for _, item := range c {
			switch b := item.(type) {
			case string:
				if b != "" {
					parts = append(parts, b)
				}
			case map[string]any:
				kind, _ := b["type"].(string)
				text, _ := b["text"].(string)
				switch kind {
				case "", "text":
					if text != "" {
						parts = append(parts, text)
					}
				case "image":
					parts = append(parts, "[image]")
				case "file":
					name, _ := b["name"].(string)
					if name == "" {
						name, _ = b["path"].(string)
					}
					parts = append(parts, "[file "+name+"]")
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// stringField reads a string field from a payload, or "" when absent.
func stringField(p map[string]any, key string) string {
	if s, ok := p[key].(string); ok {
		return s
	}
	return ""
}

// jsonOneLine is a payload as one line of JSON, for the event rows. Empty
// payloads render as no detail at all.
func jsonOneLine(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(string(b), "\n", " ")
}

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

// blockLines lays one block out as terminal lines. A non-chat row is a single
// prefixed line. A conversation block either stays inline on the prefix line (the
// human's short message) or puts the speaker on its own line and starts the body
// below — plain text, or rendered markdown for a worker's answer.
func blockLines(r rendered, width int) []string {
	if !r.chat {
		text := fitOneLine(r.text, width-displayWidth(r.plainPrefix)-1)
		return []string{r.prefix + r.textStyle(text)}
	}
	if r.inline {
		return inlineLines(r, width)
	}
	lines := []string{r.prefix}
	if r.text == "" {
		return lines
	}
	if r.markdown {
		return append(lines, renderMarkdown(r.text, width)...)
	}
	budget := width
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
			lines = append(lines, r.textStyle(line))
			rs = trimLeadingSpaces(rs[cut:])
		}
	}
	return lines
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

// runeCells is how many terminal cells one rune occupies.
func runeCells(r rune) int {
	if isWide(r) {
		return 2
	}
	return 1
}

// truncateCells cuts s to at most max cells, never splitting a wide rune.
func truncateCells(s string, max int) string {
	var b strings.Builder
	cells := 0
	for _, r := range s {
		w := runeCells(r)
		if cells+w > max {
			break
		}
		cells += w
		b.WriteRune(r)
	}
	return b.String()
}
