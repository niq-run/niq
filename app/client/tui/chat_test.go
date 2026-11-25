package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/niq-run/niq/app/standalone/contract"
)

// TestBottomHintPersists checks the shortcut hint is repainted with the bottom
// region on every transcript write, so it never scrolls away: drawn once at
// start, then again after each committed block.
func TestBottomHintPersists(t *testing.T) {
	var buf bytes.Buffer
	c := newChat(&buf, 60)
	c.header("p", "w", 1)
	c.drawBottom()

	user, _ := describe(contract.Event{Type: "worker.input", Payload: map[string]any{"content": []any{"hi"}}}, 60)
	c.commit(user)
	resp, _ := describe(contract.Event{Type: "reason.response", WorkerID: "w", Payload: map[string]any{"content": []any{"yo"}}}, 60)
	c.commit(resp)

	if n := strings.Count(buf.String(), "ctrl+t target"); n != 3 {
		t.Fatalf("hint drawn %d times, want 3 (initial + one per block)", n)
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
	if r, ok := describe(reply, 80); !ok || r.text != "line one\nline two" || !strings.HasPrefix(r.plainPrefix, "reason") || !r.chat {
		t.Fatalf("reply = %+v ok=%v", r, ok)
	}
}

// TestDescribeDropsNoise pins the events a conversation hides: delivery markers,
// deltas and lifecycle presence.
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
	lines := blockLines(r, 11)
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
	got := blockLines(para, 40)
	if len(got) != 4 || got[0] != "you › " || got[1] != "a" || got[2] != "" || got[3] != "b" {
		t.Fatalf("paragraph = %q", got)
	}
}

// TestEventLineIsSingleLine pins the event-row rule: one line, a coloured label
// followed by a colon, and an overlong detail truncated with an ellipsis rather
// than wrapped.
func TestEventLineIsSingleLine(t *testing.T) {
	r := eventLine("thinking", strings.Repeat("word ", 200), evThinking, 40)
	lines := blockLines(r, 40)
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
func TestEventLineLabels(t *testing.T) {
	for _, tc := range []struct {
		ev   contract.Event
		want string
	}{
		{contract.Event{Type: "reason.thinking", Payload: map[string]any{"content": []any{"ponder"}}}, "thinking: "},
		{contract.Event{Type: "worker.abort"}, "abort: "},
		{contract.Event{Type: "timer.timeout"}, "timeout: "},
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
		if len(blockLines(r, 80)) != 1 {
			t.Errorf("%s is not one line", tc.ev.Type)
		}
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
	if lines := blockLines(r, 40); len(lines) != 1 || lines[0] != "you › hi there" {
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
	if lines := blockLines(r, 80); len(lines) != 1 || !strings.HasPrefix(lines[0], "you → niq › hello") {
		t.Fatalf("lines = %q", lines)
	}
}

// TestHintShowsTarget checks the pinned hint names the current send target.
func TestHintShowsTarget(t *testing.T) {
	c := newChat(&bytes.Buffer{}, 80)
	c.target = "blank-slate"
	line := c.hintLine()
	if !strings.Contains(line, "→ blank-slate") {
		t.Fatalf("hint missing target: %q", line)
	}
	if !strings.Contains(line, "ctrl+t") {
		t.Fatalf("hint missing shortcuts: %q", line)
	}
}

// TestMarkdownRendering checks that a formatted answer loses its raw markers and
// gains structure: headings, list bullets, a quote/code bar, and no leftover **,
// backticks or link URLs. It also pins that no line exceeds the width.
func TestMarkdownRendering(t *testing.T) {
	src := strings.Join([]string{
		"## 职责",
		"主 reasoning worker，负责：",
		"",
		"- **任务推进与拆解**：把长任务切成可以推进的小步",
		"- 使用 `run_program` 执行脚本",
		"1. 第一步",
		"2. 第二步",
		"",
		"> 引用一句",
		"",
		"```go",
		`fmt.Println("hi")`,
		"```",
		"",
		"[链接](https://example.com)",
	}, "\n")
	lines := renderMarkdown(src, 50)
	t.Logf("\n%s", strings.Join(lines, "\n"))

	joined := strings.Join(lines, "\n")
	for _, bad := range []string{"**", "`", "##", "https://example.com"} {
		if strings.Contains(joined, bad) {
			t.Errorf("markdown marker %q survived:\n%s", bad, joined)
		}
	}
	for _, want := range []string{"职责", "任务推进与拆解", "run_program", "• ", "1. 第一步", "│ 引用一句", "│ fmt.Println", "链接"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	for i, ln := range lines {
		if w := displayWidth(ln); w > 50 {
			t.Errorf("line %d is %d cells, want <= 50: %q", i, w, ln)
		}
	}
}

// TestMarkdownTable checks a pipe table is drawn with box separators, no raw
// pipes remain, alignment is preserved, and every line fits the width.
func TestMarkdownTable(t *testing.T) {
	src := strings.Join([]string{
		"| 名称 | 值 |",
		"|:-----|----:|",
		"| alpha | 1 |",
		"| 很长的一个单元格 | 2222 |",
	}, "\n")
	lines := renderMarkdown(src, 40)
	t.Logf("\n%s", strings.Join(lines, "\n"))

	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "|") {
		t.Errorf("raw pipe survived:\n%s", joined)
	}
	for _, want := range []string{"名称", "值", "alpha", "2222", "│", "┼"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	for i, ln := range lines {
		if w := displayWidth(ln); w > 40 {
			t.Errorf("table line %d is %d cells, want <= 40: %q", i, w, ln)
		}
	}
}

// TestCodeBlockRendering checks a fenced block is rendered under a bar with the
// language recognised (text preserved when colour is off in tests).
func TestCodeBlockRendering(t *testing.T) {
	src := "before\n\n```go\nfunc main() {\n\tprintln(41 + 1)\n}\n```\n\nafter"
	lines := renderMarkdown(src, 60)
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "```") {
		t.Fatalf("fence survived:\n%s", joined)
	}
	if !strings.Contains(joined, "│ func main() {") {
		t.Fatalf("code not rendered under a bar:\n%s", joined)
	}
	if !strings.Contains(joined, "println(41 + 1)") {
		t.Fatalf("code text lost:\n%s", joined)
	}
}

// TestCodeHighlightStyles enables colour and checks the four token classes get
// distinct ANSI sequences, then restores the colour flag for other tests.
func TestCodeHighlightStyles(t *testing.T) {
	old := ansi
	ansi = true
	defer func() { ansi = old }()

	hl := newHighlighter("go")
	if out := hl.line(`func main() { s := "hi" }`); !strings.Contains(out, "\x1b[1;36m") || !strings.Contains(out, "\x1b[32m") {
		t.Fatalf("keyword/string not highlighted: %q", out)
	}
	if out := newHighlighter("go").line(`x := 42`); !strings.Contains(out, "\x1b[33m") {
		t.Fatalf("number not highlighted: %q", out)
	}
	if out := newHighlighter("go").line(`// a comment`); !strings.Contains(out, "\x1b[2m") {
		t.Fatalf("comment not highlighted: %q", out)
	}

	// A block comment carries across lines.
	block := newHighlighter("go")
	block.line(`/* start`)
	if out := block.line(`middle`); !strings.Contains(out, "\x1b[2m") {
		t.Fatalf("block comment continuation not highlighted: %q", out)
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
	if e.text() != "abXc" || e.cur != 3 {
		t.Fatalf("after insert: %q cur=%d", e.text(), e.cur)
	}
	e.backspace() // removes X
	if e.text() != "abc" || e.cur != 2 {
		t.Fatalf("after backspace: %q cur=%d", e.text(), e.cur)
	}
	e.delete() // removes c (at cursor)
	if e.text() != "ab" || e.cur != 2 {
		t.Fatalf("after delete: %q cur=%d", e.text(), e.cur)
	}
	e.home()
	if e.cur != 0 {
		t.Fatalf("home cur=%d", e.cur)
	}
	e.backspace() // no-op at start
	e.delete()    // removes a
	if e.text() != "b" {
		t.Fatalf("delete at start: %q", e.text())
	}
	e.end()
	if e.cur != len(e.buf) {
		t.Fatalf("end cur=%d", e.cur)
	}
	e.clear()
	if e.text() != "" || e.cur != 0 {
		t.Fatalf("clear: %q cur=%d", e.text(), e.cur)
	}
}

// TestEditorWindow pins the input's horizontal window: the cursor is always
// inside it, and the shown slice is bounded by avail cells.
func TestEditorWindow(t *testing.T) {
	// Cursor at the end of a long line: the window shows the tail, width-bounded.
	e := &editor{buf: []rune("abcdefghijklmnopqrst"), cur: 20}
	start, end, cur := e.window(10)
	if start != 11 || end != 20 || cur != 9 {
		t.Fatalf("tail window = (%d,%d,%d)", start, end, cur)
	}
	if w := displayWidth(string(e.buf[start:end])); w > 10 {
		t.Fatalf("window width %d exceeds 10", w)
	}
	// Cursor at the start: the window fills forward.
	e.cur = 0
	start, end, cur = e.window(10)
	if start != 0 || end != 10 || cur != 0 {
		t.Fatalf("head window = (%d,%d,%d)", start, end, cur)
	}
	// A short line fits entirely.
	short := &editor{buf: []rune("hi"), cur: 2}
	if s, en, c := short.window(10); s != 0 || en != 2 || c != 2 {
		t.Fatalf("short window = (%d,%d,%d)", s, en, c)
	}
}
