package render

import (
	"strings"
	"testing"

	"github.com/niq-run/niq/app/client/tui/term"
)

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
	lines := RenderMarkdown(src, 50)
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
		if w := term.DisplayWidth(ln); w > 50 {
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
	lines := RenderMarkdown(src, 40)
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
		if w := term.DisplayWidth(ln); w > 40 {
			t.Errorf("table line %d is %d cells, want <= 40: %q", i, w, ln)
		}
	}
}

// TestCodeBlockRendering checks a fenced block is rendered under a bar with the
// language recognised (text preserved when colour is off in tests).
func TestCodeBlockRendering(t *testing.T) {
	src := "before\n\n```go\nfunc main() {\n\tprintln(41 + 1)\n}\n```\n\nafter"
	lines := RenderMarkdown(src, 60)
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
	old := term.Ansi
	term.Ansi = true
	defer func() { term.Ansi = old }()

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
