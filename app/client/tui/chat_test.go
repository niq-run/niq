package tui

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/layout"
	"github.com/niq-run/niq/app/client/tui/screen"
	"github.com/niq-run/niq/app/client/tui/term"
	"github.com/niq-run/niq/app/standalone/contract"
)

// TestDecodeMouseWheel checks SGR wheel reports map to scrolling and the left
// button maps to the selection gesture.
func TestDecodeMouseWheel(t *testing.T) {
	if k := decodeMouse(bufio.NewReader(strings.NewReader("64;10;5M"))); k.Kind != pressWheelUp {
		t.Fatalf("wheel up = %v", k.Kind)
	}
	if k := decodeMouse(bufio.NewReader(strings.NewReader("65;10;5M"))); k.Kind != pressWheelDown {
		t.Fatalf("wheel down = %v", k.Kind)
	}
	if k := decodeMouse(bufio.NewReader(strings.NewReader("0;10;5M"))); k.Kind != pressClick || k.X != 10 || k.Y != 5 {
		t.Fatalf("left click = %+v, want click at 10,5", k)
	}
	if k := decodeMouse(bufio.NewReader(strings.NewReader("32;12;6M"))); k.Kind != pressDrag || k.X != 12 || k.Y != 6 {
		t.Fatalf("left drag = %+v", k)
	}
	if k := decodeMouse(bufio.NewReader(strings.NewReader("0;12;6m"))); k.Kind != pressRelease {
		t.Fatalf("left release = %+v", k)
	}
	if k := decodeMouse(bufio.NewReader(strings.NewReader("2;10;5M"))); k.Kind != pressOther {
		t.Fatalf("right click = %v, want other", k.Kind)
	}
}

// TestHintShowsTarget checks the pinned hint names the send target, the view
// scope, and the paging keys.
func TestHintShowsTarget(t *testing.T) {
	c := &chat{width: 120, target: "blank-slate", vp: layout.Viewport{Follow: true}}
	h := c.hint()
	if !strings.Contains(h, "target: blank-slate") {
		t.Fatalf("hint missing target: %q", h)
	}
	if !strings.Contains(h, "view: all") {
		t.Fatalf("hint missing view: %q", h)
	}
	if !strings.Contains(h, "insert") {
		t.Fatalf("hint missing the mode state: %q", h)
	}
}

// TestViewportPaging pins the chat's scroll wiring: a page up unfollows and
// moves a full viewport; paging down past the bottom re-follows.
func TestViewportPaging(t *testing.T) {
	c := &chat{height: 12, vp: layout.Viewport{Follow: true}}
	c.lines = make([]string, 100)
	c.vp.Resize(c.viewHeight())
	c.vp.SetTotal(len(c.lines))
	vh := c.vp.VisibleHeight()

	c.vp.PageUp()
	if c.vp.Follow || c.vp.Top != 100-2*vh {
		t.Fatalf("pageUp: top=%d follow=%v (vh=%d)", c.vp.Top, c.vp.Follow, vh)
	}
	for i := 0; i < 20; i++ {
		c.vp.PageUp()
	}
	if c.vp.Top != 0 {
		t.Fatalf("pageUp clamp: top=%d", c.vp.Top)
	}
	c.vp.PageDown()
	if c.vp.Top != vh || c.vp.Follow {
		t.Fatalf("pageDown: top=%d follow=%v", c.vp.Top, c.vp.Follow)
	}
	for i := 0; i < 20; i++ {
		c.vp.PageDown()
	}
	if !c.vp.Follow || c.vp.Top != 100-vh {
		t.Fatalf("pageDown clamp: top=%d follow=%v", c.vp.Top, c.vp.Follow)
	}
}

// TestMergeHistory pins chronological prepend and id dedupe: the server returns
// newest-first, and an event seen live must not appear twice.
func TestMergeHistory(t *testing.T) {
	c := &chat{seen: map[string]bool{"b": true}}
	c.events = []contract.Event{{ID: "b"}, {ID: "c"}}
	n := c.mergeHistory([]contract.Event{{ID: "a2"}, {ID: "a1"}, {ID: "b"}})
	if n != 2 {
		t.Fatalf("merged %d, want 2", n)
	}
	var ids []string
	for _, ev := range c.events {
		ids = append(ids, ev.ID)
	}
	if strings.Join(ids, ",") != "a1,a2,b,c" {
		t.Fatalf("order = %v", ids)
	}
}

// TestMatchesFilter pins the worker scope: sender, single target, or recipient.
func TestMatchesFilter(t *testing.T) {
	ev := contract.Event{WorkerID: "reason", Recipients: []string{"webui"}}
	if !matchesFilter(ev, "") || !matchesFilter(ev, "reason") || !matchesFilter(ev, "webui") {
		t.Fatal("expected a match")
	}
	if matchesFilter(ev, "other") {
		t.Fatal("unexpected match")
	}
	if !matchesFilter(contract.Event{TargetWorkerID: "x"}, "x") {
		t.Fatal("target match")
	}
}

// TestClipStyled pins width clipping: ANSI sequences are preserved and not
// counted, wide runes are never split, and a cut closes the open style.
func TestClipStyled(t *testing.T) {
	got := clipStyled("\x1b[1mhello world\x1b[0m", 5)
	if !strings.HasPrefix(got, "\x1b[1mhello") {
		t.Fatalf("clip = %q", got)
	}
	if !strings.Contains(got, "\x1b[0m") {
		t.Fatalf("clipped style not closed: %q", got)
	}
	cjk := clipStyled("一二三四", 5)
	if plain := stripANSI(cjk); plain != "一二" {
		t.Fatalf("cjk clip = %q (plain %q)", cjk, plain)
	}
}

// TestRenderEventsFilter pins the transcript scope: a worker filter keeps only
// events the worker sent, received, or was targeted by.
func TestRenderEventsFilter(t *testing.T) {
	events := []contract.Event{
		{ID: "1", Type: "worker.input", WorkerID: "webui", Recipients: []string{"niq"}, Payload: map[string]any{"content": []any{"hi"}}},
		{ID: "2", Type: "reason.response", WorkerID: "niq", Payload: map[string]any{"content": []any{"yo"}}},
		{ID: "3", Type: "reason.response", WorkerID: "other", Payload: map[string]any{"content": []any{"nope"}}},
	}
	scoped := strings.Join(renderEvents(events, "niq", 60), "\n")
	if !strings.Contains(scoped, "hi") || !strings.Contains(scoped, "yo") {
		t.Fatalf("scoped transcript missing conversation:\n%s", scoped)
	}
	if strings.Contains(scoped, "nope") {
		t.Fatalf("scoped transcript leaked another worker:\n%s", scoped)
	}
	if !strings.Contains(strings.Join(renderEvents(events, "", 60), "\n"), "nope") {
		t.Fatal("unfiltered transcript is missing an event")
	}
}

func stripANSI(s string) string {
	var b strings.Builder
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
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// TestBandLinePads checks a band fills the row exactly, keeps its inner margin,
// and carries the background sequence, with inner SGR resets re-applying it.
func TestBandLinePads(t *testing.T) {
	oldA, oldT := term.Ansi, term.Truecolor
	term.Ansi, term.Truecolor = true, true
	defer func() { term.Ansi, term.Truecolor = oldA, oldT }()

	bg := bandUser.Bg()
	line := bandLine(bold("hi")+" there", 20, 1, bg)
	if w := visibleWidth(line); w != 20 {
		t.Fatalf("band width = %d, want 20", w)
	}
	if !strings.Contains(line, "48;2;") {
		t.Fatalf("band missing background: %q", line)
	}
	if plain := stripANSI(line); !strings.HasPrefix(plain, " ") || len(plain) != 20 {
		t.Fatalf("band inner margin/width = %q", plain)
	}
	// The background must survive the inner bold reset.
	if strings.Count(line, bg) < 2 {
		t.Fatalf("background not re-applied after a reset: %q", line)
	}
}

// TestDockHasNoSurface pins that the input area carries no background of its own:
// the two rules frame it, and the composer rows stay on the terminal background.
// Banded blocks (a tool card here) still paint their own surface.
func TestDockHasNoSurface(t *testing.T) {
	oldAnsi, oldTC := term.Ansi, term.Truecolor
	defer func() { term.Ansi, term.Truecolor = oldAnsi, oldTC }()
	term.Ansi, term.Truecolor = true, true

	var buf bytes.Buffer
	scr := screen.NewScreen(&buf, 0)
	c := &chat{
		scr: scr, width: scr.W, height: scr.H,
		in: newInput(), partners: []contract.Worker{{ID: "niq", Type: "reason", Online: true}},
		target: "niq", vp: layout.Viewport{Follow: true}, seen: map[string]bool{},
	}
	c.lines = []string{"\x1b[38;2;226;232;240;48;2;37;65;49m$ read → workspace\x1b[0m"}
	c.render()
	c.render() // second frame: the diff renderer only writes changed rows

	out := buf.String()
	if !strings.Contains(out, "48;2;37;65;49") {
		t.Fatalf("the tool card lost its surface:\n%q", out)
	}
	// Exactly one surface in the frame: the card. The hint, the composer row and
	// the closing rule all stay on the terminal's own background.
	if n := strings.Count(out, "48;2;"); n != 1 {
		t.Fatalf("frame has %d painted surfaces, want only the tool card:\n%q", n, out)
	}
}

// TestDockFillsWidth checks the status rule and the closing rule span the row.
func TestDockFillsWidth(t *testing.T) {
	c := &chat{width: 60, target: "blank-slate", vp: layout.Viewport{Follow: true}}
	if w := visibleWidth(c.hint()); w != 60 {
		t.Fatalf("hint width = %d, want 60", w)
	}
	if w := visibleWidth(c.rule()); w != 60 {
		t.Fatalf("rule width = %d, want 60", w)
	}
	narrow := &chat{width: 20, target: "blank-slate", vp: layout.Viewport{Follow: true}}
	if w := visibleWidth(narrow.hint()); w > 20 {
		t.Fatalf("narrow hint width = %d, want <= 20", w)
	}
}

// TestUserBlockBandedWidth checks a user block is painted as a full-width card
// with a top/bottom padding row (every line spans the row).
func TestUserBlockBandedWidth(t *testing.T) {
	oldA, oldT := term.Ansi, term.Truecolor
	term.Ansi, term.Truecolor = true, true
	defer func() { term.Ansi, term.Truecolor = oldA, oldT }()

	r, _ := describe(contract.Event{Type: "worker.input", Recipients: []string{"niq"}, Payload: map[string]any{"content": []any{"hello there friend"}}}, 40)
	lines := renderBlock(blockBody(r, 40), r.band, 40)
	if len(lines) < 3 {
		t.Fatalf("expected padding + content, got %d lines", len(lines))
	}
	for i, ln := range lines {
		if w := visibleWidth(ln); w != 40 {
			t.Fatalf("user line %d width = %d, want 40", i, w)
		}
	}
}

// TestTalkPartners pins who a message can be addressed to: the reason family and
// the niq link, never the interface / workspace / tool workers.
func TestTalkPartners(t *testing.T) {
	workers := []contract.Worker{
		{ID: "webui", Type: "interface"},
		{ID: "reason", Type: "reason"},
		{ID: "ws", Type: "workspace"},
		{ID: "code", Type: "coding"},
		{ID: "link", Type: "niq"},
	}
	got := talkPartners(workers)
	var ids []string
	for _, w := range got {
		ids = append(ids, w.ID)
	}
	if strings.Join(ids, ",") != "reason,code,link" {
		t.Fatalf("partners = %v", ids)
	}
}

// TestPickTarget prefers an online partner, and falls back to the first when all
// are offline.
func TestPickTarget(t *testing.T) {
	partners := []contract.Worker{{ID: "a", Type: "reason"}, {ID: "b", Type: "reason", Online: true}}
	if got := pickTarget(partners); got != "b" {
		t.Fatalf("pickTarget = %q, want b (online)", got)
	}
	offline := []contract.Worker{{ID: "a", Type: "reason"}, {ID: "b", Type: "reason"}}
	if got := pickTarget(offline); got != "a" {
		t.Fatalf("pickTarget = %q, want a (first fallback)", got)
	}
	if got := pickTarget(nil); got != "" {
		t.Fatalf("pickTarget(nil) = %q", got)
	}
}

// TestDescribeConversation checks the two sides of the conversation render with
// their speaker prefix, joined text, and the chat marker the view uses to add
// spacing.
func TestDescribeConversation(t *testing.T) {
	user := contract.Event{Type: "worker.input", WorkerID: "webui", Payload: map[string]any{
		"content": []any{map[string]any{"type": "text", "text": "hello"}},
	}}
	if r, ok := describe(user, 80); !ok || r.text != "hello" || !strings.HasPrefix(r.plainPrefix, "you") || !r.chat {
		t.Fatalf("user = %+v ok=%v", r, ok)
	}

	reply := contract.Event{Type: "reason.response", WorkerID: "reason", Payload: map[string]any{
		"content": []any{"line one", "line two"},
	}}
	if r, ok := describe(reply, 80); !ok || r.text != "line one\nline two" || !strings.Contains(r.header, "reason") || !r.chat {
		t.Fatalf("reply = %+v ok=%v", r, ok)
	}
}

// TestResponseSectionRule pins the response header: a full-width rule with the
// speaker's name set into it, a few dashes in from the left.
func TestResponseSectionRule(t *testing.T) {
	r, ok := describe(contract.Event{Type: "reason.response", WorkerID: "blank-slate",
		Payload: map[string]any{"content": []any{"hi"}}}, 56)
	if !ok {
		t.Fatal("response dropped")
	}
	if w := visibleWidth(r.header); w != 56 {
		t.Fatalf("rule width = %d, want 56: %q", w, r.header)
	}
	plain := stripANSI(r.header)
	if !strings.HasPrefix(plain, "blank-slate ···") {
		t.Fatalf("rule = %q", plain)
	}
	short := sectionRule("x", 9, 0)
	if w := visibleWidth(short); w != 9 {
		t.Fatalf("short rule width = %d, want 9: %q", w, short)
	}
}

// TestWarnRowsAreBanded checks a warning row (cancel, timeout, abort) now has its
// own amber surface rather than floating on the terminal background.
func TestWarnRowsAreBanded(t *testing.T) {
	for _, typ := range []string{"request.cancel", "timer.timeout", "worker.abort", "reason.interrupted"} {
		r, ok := describe(contract.Event{Type: typ}, 80)
		if !ok {
			t.Fatalf("%s dropped", typ)
		}
		if r.band != bandWarn {
			t.Fatalf("%s band = %v, want bandWarn", typ, r.band)
		}
	}
}

// TestReplyInset checks a worker's answer is unbanded, its rule starts at the left
// edge, its body is indented like the text in the banded cards, and a blank row
// separates the two.
func TestReplyInset(t *testing.T) {
	oldAnsi, oldTC := term.Ansi, term.Truecolor
	defer func() { term.Ansi, term.Truecolor = oldAnsi, oldTC }()
	term.Ansi, term.Truecolor = true, true

	events := []contract.Event{
		{ID: "t1", Type: "reason.thinking", WorkerID: "niq", Payload: map[string]any{"content": []any{"hmm"}}},
		{ID: "r1", Type: "reason.response", WorkerID: "niq", Payload: map[string]any{"content": []any{"answer"}}},
	}
	lines := renderEvents(events, "", 40)

	var ruled, blank, body bool
	for _, ln := range lines {
		if strings.Contains(ln, "48;2;") {
			t.Fatalf("reply block still banded: %q", ln)
		}
		plain := stripANSI(ln)
		switch {
		case strings.Contains(plain, "niq ·"):
			// Flush left and full width.
			ruled = strings.HasPrefix(plain, "niq ·") && visibleWidth(ln) == 40
		case ruled && strings.TrimSpace(plain) == "":
			blank = true
		case strings.Contains(plain, "answer"):
			body = strings.HasPrefix(plain, " answer")
		}
	}
	if !ruled || !blank || !body {
		t.Fatalf("reply section incomplete: rule=%v blank=%v body=%v", ruled, blank, body)
	}
}

// TestToolCallCard pins the tool card's shape: the call line (name, target and
// argument payload), a spacer, then at most two reply rows with the overflow
// ellipsized. The reply event is folded into the card, not drawn twice.
func TestToolCallCard(t *testing.T) {
	call := contract.Event{ID: "c1", Type: "read", WorkerID: "niq",
		TargetWorkerID: "workspace", RequestID: "r1",
		Payload: map[string]any{"path": "README.md"}}
	done := contract.Event{ID: "a1", Type: "request.completed", WorkerID: "workspace",
		RequestID: "r1", Payload: map[string]any{"result": "alpha\nbeta\ngamma\ndelta"}}

	lines := renderEvents([]contract.Event{call, done}, "", 60)
	if len(lines) != 4 {
		t.Fatalf("card lines = %d, want 4: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "read") || !strings.Contains(lines[0], "→ workspace") ||
		!strings.Contains(lines[0], "README.md") {
		t.Fatalf("call line = %q", lines[0])
	}
	if lines[1] != "" {
		t.Fatalf("no spacer between call and reply: %q", lines)
	}
	if lines[2] != "alpha" || !strings.HasPrefix(lines[3], "beta") || !strings.HasSuffix(lines[3], "…") {
		t.Fatalf("reply rows = %q", lines[2:])
	}
	if strings.Contains(strings.Join(lines, "\n"), "alpha\nalpha") {
		t.Fatalf("reply drawn twice: %q", lines)
	}
}

// TestToolCallCardsStandAlone pins that two calls are two blocks with a gap, not
// one merged panel.
func TestToolCallCardsStandAlone(t *testing.T) {
	events := []contract.Event{
		{ID: "c1", Type: "read", TargetWorkerID: "workspace", RequestID: "r1"},
		{ID: "a1", Type: "request.completed", RequestID: "r1", Payload: map[string]any{"result": "one"}},
		{ID: "c2", Type: "bash", TargetWorkerID: "host", RequestID: "r2"},
		{ID: "a2", Type: "request.completed", RequestID: "r2", Payload: map[string]any{"result": "two"}},
	}
	lines := renderEvents(events, "", 60)
	// card one: head, spacer, reply; gap; card two: head, spacer, reply.
	if len(lines) != 7 {
		t.Fatalf("two cards = %d lines, want 7: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "read") || lines[3] != "" || !strings.Contains(lines[4], "bash") {
		t.Fatalf("cards not separated: %q", lines)
	}
}

// TestToolFailureSurface checks a failed reply paints the card on the error
// surface and keeps its error text.
func TestToolFailureSurface(t *testing.T) {
	call := contract.Event{ID: "c1", Type: "bash", TargetWorkerID: "host", RequestID: "r1"}
	failed := contract.Event{ID: "a1", Type: "request.failed", RequestID: "r1",
		Payload: map[string]any{"error": "exit 1: not a git repository"}}
	r := toolBlock(call, &failed, 60)
	if r.band != bandError {
		t.Fatalf("band = %v, want bandError", r.band)
	}
	if got := strings.Join(r.body, "\n"); !strings.Contains(got, "not a git repository") {
		t.Fatalf("error text missing: %q", got)
	}
}

// TestToolOrphanAnswer checks a reply whose call is out of view still renders as
// a row rather than disappearing.
func TestToolOrphanAnswer(t *testing.T) {
	done := contract.Event{ID: "a1", Type: "request.completed", RequestID: "gone",
		Payload: map[string]any{"result": "kept"}}
	lines := renderEvents([]contract.Event{done}, "", 60)
	if len(lines) == 0 || !strings.Contains(strings.Join(lines, "\n"), "request.completed") {
		t.Fatalf("orphan answer dropped: %q", lines)
	}
}

// TestWrapMax pins the three-row budget: two rows of reply, ellipsized.
func TestWrapMax(t *testing.T) {
	got := wrapMax("aaa\nbbb\nccc", 20, 2)
	if len(got) != 2 || got[0] != "aaa" || got[1] != "bbb…" {
		t.Fatalf("wrapMax = %q", got)
	}
	if got := wrapMax("", 20, 2); got != nil {
		t.Fatalf("empty wrapMax = %q", got)
	}
	lang := wrapMax(strings.Repeat("x", 50), 10, 3)
	if len(lang) != 3 {
		t.Fatalf("long wrap = %d rows, want 3", len(lang))
	}
	for _, l := range lang {
		if displayWidth(l) > 10 {
			t.Fatalf("row too wide: %q", l)
		}
	}
}

func TestDescribeDropsNoise(t *testing.T) {
	for _, typ := range []string{"event.delivered", "reason.text_delta", "reason.thinking_delta", "worker.ready", "worker.gone", "request.progressed", "reason.start"} {
		if _, ok := describe(contract.Event{Type: typ}, 80); ok {
			t.Errorf("%s was shown", typ)
		}
	}
	if _, ok := describe(contract.Event{Type: "reason.response", Transient: true}, 80); ok {
		t.Error("a transient event was shown")
	}
}

// TestBlockLinesWrap pins the conversation layout: the speaker prefix is its own
// line, the body starts on the next line flush left (no left gap), an empty
// paragraph stays empty, and CJK wraps per character.
func TestBlockLinesWrap(t *testing.T) {
	r := rendered{plainPrefix: "you › ", prefix: "you › ", text: "hello world again", textStyle: plain, chat: true}
	lines := blockBody(r, 11)
	if len(lines) != 3 || lines[0] != "you › " || lines[1] != "hello world" || lines[2] != "again" {
		t.Fatalf("wrap = %q", lines)
	}
	for i, ln := range lines[1:] {
		if strings.HasPrefix(ln, " ") {
			t.Fatalf("body line %d has a left gap: %q", i+1, ln)
		}
	}

	// CJK breaks per character and fills each line.
	if got := breakAt([]rune("一二三四五"), 8); got != 4 {
		t.Fatalf("cjk breakAt = %d, want 4", got)
	}

	// A blank paragraph is a truly empty line, not indented whitespace.
	para := rendered{plainPrefix: "you › ", prefix: "you › ", text: "a\n\nb", textStyle: plain, chat: true}
	got := blockBody(para, 40)
	if len(got) != 4 || got[0] != "you › " || got[1] != "a" || got[2] != "" || got[3] != "b" {
		t.Fatalf("paragraph = %q", got)
	}
}

// TestEventLineIsSingleLine pins the event-row rule: one line, a coloured label
// followed by a colon, and an overlong detail truncated with an ellipsis rather
// than wrapped.
func TestEventLineIsSingleLine(t *testing.T) {
	r := eventLine("thinking", strings.Repeat("word ", 200), evThinking, 40)
	lines := blockBody(r, 40)
	if len(lines) != 1 {
		t.Fatalf("event row produced %d lines: %q", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "thinking: ") {
		t.Fatalf("missing label: %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], "…") {
		t.Fatalf("long detail not ellipsized: %q", lines[0])
	}
	if w := displayWidth(lines[0]); w > 40 {
		t.Fatalf("event row %d cells wide, want <= 40", w)
	}
}

// TestEventLineLabels covers the label: form the event families produce.
// TestEventLineLabels covers the label: form the event families produce. Thinking
// is its own shape now (a grey block, see TestThinkingBlock), so it is not here.
func TestEventLineLabels(t *testing.T) {
	for _, tc := range []struct {
		ev   contract.Event
		want string
	}{
		{contract.Event{Type: "worker.abort"}, "abort"},
		{contract.Event{Type: "timer.timeout"}, "timeout"},
		{contract.Event{Type: "request.failed", Payload: map[string]any{"error": "boom"}}, "request.failed: "},
		{contract.Event{Type: "search", Payload: map[string]any{"q": "x"}}, "search: "},
	} {
		r, ok := describe(tc.ev, 80)
		if !ok {
			t.Fatalf("%s was dropped", tc.ev.Type)
		}
		if !strings.HasPrefix(r.plainPrefix, tc.want) {
			t.Errorf("%s prefix = %q, want %q", tc.ev.Type, r.plainPrefix, tc.want)
		}
		if len(blockBody(r, 80)) != 1 {
			t.Errorf("%s is not one line", tc.ev.Type)
		}
	}
}

// TestTimeoutAndCancelDetails checks the timeout and cancel rows name what
// happened instead of showing a bare label.
func TestTimeoutAndCancelDetails(t *testing.T) {
	// A timer with a purpose names it.
	r, ok := describe(contract.Event{Type: "timer.timeout", Payload: map[string]any{
		"result": `{"purpose":"round watchdog","duration_ms":600000}`,
	}}, 80)
	if !ok || r.plainPrefix != "timeout: " || r.text != "round watchdog" {
		t.Fatalf("timeout row = %q / %q", r.plainPrefix, r.text)
	}

	// With no purpose it falls back to the armed duration.
	r, _ = describe(contract.Event{Type: "timer.timeout", Payload: map[string]any{
		"result": `{"duration_ms":600000}`,
	}}, 80)
	if r.text != "10m0s" {
		t.Fatalf("timeout duration = %q", r.text)
	}

	// A cancel names the timer it cancelled.
	r, ok = describe(contract.Event{Type: "request.cancel", Payload: map[string]any{
		"timer_id": "niq_wd_7",
	}}, 80)
	if !ok || r.plainPrefix != "cancel: " || r.text != "niq_wd_7" {
		t.Fatalf("cancel row = %q / %q", r.plainPrefix, r.text)
	}
}

// TestFitOneLine collapses whitespace and truncates with an ellipsis.
func TestFitOneLine(t *testing.T) {
	if got := fitOneLine("a\n\nb   c", 40); got != "a b c" {
		t.Fatalf("collapse = %q", got)
	}
	if got := fitOneLine(strings.Repeat("x", 10), 5); got != "xxxx…" {
		t.Fatalf("truncate = %q", got)
	}
	if got := fitOneLine("short", 40); got != "short" {
		t.Fatalf("short = %q", got)
	}
}

// TestWorkerNameAndIndex covers the helpers the target picker leans on.
func TestWorkerNameAndIndex(t *testing.T) {
	ws := []contract.Worker{{ID: "a"}, {ID: "b", Label: "Bee"}}
	if got := workerName(ws[0]); got != "a" {
		t.Fatalf("workerName(id only) = %q", got)
	}
	if got := workerName(ws[1]); got != "Bee" {
		t.Fatalf("workerName(label) = %q", got)
	}
	if got := indexOfWorker(ws, "b"); got != 1 {
		t.Fatalf("indexOfWorker(b) = %d", got)
	}
	if got := indexOfWorker(ws, "missing"); got != 0 {
		t.Fatalf("indexOfWorker(missing) = %d", got)
	}
}

// TestInlineUserMessage pins the human's side: it stays on the prefix line and
// wraps flush left, never starting a fresh block like a worker's answer.
func TestInlineUserMessage(t *testing.T) {
	r, ok := describe(contract.Event{Type: "worker.input", Payload: map[string]any{
		"content": []any{"hi there"},
	}}, 40)
	if !ok || !r.inline {
		t.Fatalf("user render = %+v ok=%v", r, ok)
	}
	if lines := blockBody(r, 40); len(lines) != 1 || lines[0] != "you › hi there" {
		t.Fatalf("user lines = %q", lines)
	}
}

// TestUserMessageShowsRecipients checks a directed input names its addressee
// after the speaker, from the bus-recorded recipients.
func TestUserMessageShowsRecipients(t *testing.T) {
	r, ok := describe(contract.Event{
		Type:       "worker.input",
		Recipients: []string{"niq"},
		Payload:    map[string]any{"content": []any{"hello"}},
	}, 80)
	if !ok || !strings.HasPrefix(r.plainPrefix, "you → niq › ") {
		t.Fatalf("prefix = %q", r.plainPrefix)
	}
	if lines := blockBody(r, 80); len(lines) != 1 || !strings.HasPrefix(lines[0], "you → niq › hello") {
		t.Fatalf("lines = %q", lines)
	}
}

// TestStripSystemReminder checks a user turn's injected system-reminder block is
// not shown as if the human typed it.
func TestStripSystemReminder(t *testing.T) {
	r, _ := describe(contract.Event{Type: "worker.input", Payload: map[string]any{
		"content": []any{"hello <system-reminder>secret note</system-reminder> world"},
	}}, 80)
	if strings.Contains(r.text, "system-reminder") || strings.Contains(r.text, "secret note") {
		t.Fatalf("reminder leaked: %q", r.text)
	}
	if !strings.Contains(r.text, "hello") || !strings.Contains(r.text, "world") {
		t.Fatalf("message text lost: %q", r.text)
	}
}

// TestEditor covers insertion at the cursor, deletion either side, movement, and
// clear.
func TestEditor(t *testing.T) {
	e := &editor{}
	e.insert('a')
	e.insert('b')
	e.insert('c')
	e.left()      // cursor between b and c
	e.insert('X') // a b X c
	if e.text() != "abXc" || e.col != 3 {
		t.Fatalf("after insert: %q col=%d", e.text(), e.col)
	}
	e.backspace() // removes X
	if e.text() != "abc" || e.col != 2 {
		t.Fatalf("after backspace: %q col=%d", e.text(), e.col)
	}
	e.delete() // removes c (at cursor)
	if e.text() != "ab" || e.col != 2 {
		t.Fatalf("after delete: %q col=%d", e.text(), e.col)
	}
	e.home()
	if e.col != 0 {
		t.Fatalf("home col=%d", e.col)
	}
	e.backspace() // no-op at start
	e.delete()    // removes a
	if e.text() != "b" {
		t.Fatalf("delete at start: %q", e.text())
	}
	e.end()
	if e.col != len(e.line()) {
		t.Fatalf("end col=%d", e.col)
	}
	e.clear()
	if e.text() != "" || e.col != 0 || e.row != 0 {
		t.Fatalf("clear: %q col=%d row=%d", e.text(), e.col, e.row)
	}
}

// TestEditorMultiLine pins the line operations: newline splits at the cursor,
// backspace at a line start joins upward, delete at a line end pulls down, and
// up/down keep the column when they can.
func TestEditorMultiLine(t *testing.T) {
	e := &editor{}
	for _, r := range "hello" {
		e.insert(r)
	}
	e.col = 2
	e.newline() // "he" / "llo"
	if e.row != 1 || e.col != 0 || e.text() != "he\nllo" {
		t.Fatalf("newline: %q row=%d col=%d", e.text(), e.row, e.col)
	}
	for _, r := range "X" {
		e.insert(r)
	}
	if e.text() != "he\nXllo" {
		t.Fatalf("insert after newline: %q", e.text())
	}
	e.left()
	e.left() // across the line start onto the previous line
	if e.row != 0 || e.col != 2 {
		t.Fatalf("left across lines: row=%d col=%d", e.row, e.col)
	}
	e.down()
	if e.row != 1 || e.col != 2 {
		t.Fatalf("down: row=%d col=%d", e.row, e.col)
	}
	e.up()
	if e.row != 0 || e.col != 2 {
		t.Fatalf("up: row=%d col=%d", e.row, e.col)
	}
	e.backspace() // at start of "he"? no: col=2 is the end, so it deletes 'e'
	if e.text() != "h\nXllo" {
		t.Fatalf("backspace: %q", e.text())
	}
	e.home()
	e.backspace() // at the very start: no-op
	if e.text() != "h\nXllo" {
		t.Fatalf("backspace at start: %q", e.text())
	}
	e.end()
	e.delete() // end of line 0: pulls the next line up
	if e.text() != "hXllo" || e.row != 0 || e.col != 1 {
		t.Fatalf("delete joins down: %q row=%d col=%d", e.text(), e.row, e.col)
	}
	e.end()
	e.delete() // end of the last line: no-op
	if e.text() != "hXllo" {
		t.Fatalf("delete at end: %q", e.text())
	}
	e.setText("one\ntwo\nthree")
	if e.text() != "one\ntwo\nthree" || e.row != 2 || e.col != 5 {
		t.Fatalf("setText: %q row=%d col=%d", e.text(), e.row, e.col)
	}
}

// TestWrapAtoms pins the dock's wrap: rows are width-bounded and an atom (a
// rune, or a whole chip) never splits across them.
func TestWrapAtoms(t *testing.T) {
	as := make([]atom, 0, 10)
	for _, r := range "abcdefghij" {
		as = append(as, atom{text: string(r), cells: 1, runes: 1})
	}
	groups := wrapAtoms(as, 4)
	if len(groups) != 3 {
		t.Fatalf("groups = %v", groups)
	}
	if groups[0] != [2]int{0, 4} || groups[1] != [2]int{4, 8} || groups[2] != [2]int{8, 10} {
		t.Fatalf("groups = %v", groups)
	}
	if got := wrapAtoms(nil, 10); len(got) != 1 || got[0] != [2]int{0, 0} {
		t.Fatalf("empty = %v", got)
	}
	// A 6-cell chip on its own row is not split.
	wide := wrapAtoms([]atom{{text: "ab", cells: 2, runes: 1}, {text: "[chip]", cells: 6, runes: 1}}, 4)
	if len(wide) != 2 {
		t.Fatalf("chip split: %v", wide)
	}
}

// TestInputRows pins the dock layout: the prompt on the first row, continuation
// rows inset to match, and the cursor mapped onto the right row and column.
func TestInputRows(t *testing.T) {
	in := newInput()
	in.ed.setText("hello world")
	c := &chat{width: 12, in: in}
	rows, curRow, curCol := c.inputRows()
	// avail = 10 cells, so the line wraps after 10 runes.
	if len(rows) != 2 {
		t.Fatalf("rows = %q", rows)
	}
	if !strings.Contains(rows[0], "hello worl") || !strings.Contains(rows[1], "d") {
		t.Fatalf("rows = %q", rows)
	}
	if curRow != 1 || curCol != 3 {
		t.Fatalf("cursor = (%d,%d), want (1,3)", curRow, curCol)
	}

	in.ed.setText("a\nb")
	rows, curRow, curCol = c.inputRows()
	if len(rows) != 2 || curRow != 1 || curCol != 3 {
		t.Fatalf("two lines: rows=%q cur=(%d,%d)", rows, curRow, curCol)
	}
}

// TestJumpUserMessages pins ctrl+n / ctrl+p in normal mode: from the bottom, prev
// lands on the last user message, prev again steps back one, next steps forward,
// and asking past an end flashes instead of moving.
func TestJumpUserMessages(t *testing.T) {
	c := &chat{userLines: []int{0, 10, 25}, vp: layout.Viewport{Follow: true}}
	c.vp.Resize(5)
	c.vp.SetTotal(40) // follow pins the top to the last screenful (35)

	c.jumpUser(context.Background(), nil, -1)
	if got := c.vp.First(); got != 25 {
		t.Fatalf("prev = %d, want 25", got)
	}
	c.jumpUser(context.Background(), nil, -1)
	if got := c.vp.First(); got != 10 {
		t.Fatalf("prev = %d, want 10", got)
	}
	c.jumpUser(context.Background(), nil, +1)
	if got := c.vp.First(); got != 25 {
		t.Fatalf("next = %d, want 25", got)
	}
	// Past the last message: no move, just a flash.
	c.jumpUser(context.Background(), nil, +1)
	if got := c.vp.First(); got != 25 {
		t.Fatalf("next past the end moved to %d", got)
	}
}
