package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPasteChip pins the large-paste chip: it hides the block behind one token,
// displays as a label, expands on send, and deletes as one character.
func TestPasteChip(t *testing.T) {
	in := newInput()
	body := "a\nb\nc\nd\ne\nf"
	in.paste(body)

	if got := in.text(); got != body {
		t.Fatalf("expanded text = %q, want %q", got, body)
	}
	rows, curRow, curCol := in.rows(40)
	if len(rows) != 1 || !strings.Contains(rows[0], "pasted text #1") || !strings.Contains(rows[0], "6 lines") {
		t.Fatalf("chip row = %q", rows)
	}
	if curRow != 0 || curCol != 2+displayWidth("[pasted text #1 · 6 lines]") {
		t.Fatalf("cursor = (%d,%d)", curRow, curCol)
	}
	// One backspace removes the whole chip.
	in.ed.backspace()
	if in.text() != "" {
		t.Fatalf("chip not atomic: %q", in.text())
	}
}

// TestSmallPasteIsText checks a short paste is inserted as ordinary text, newlines
// and all.
func TestSmallPasteIsText(t *testing.T) {
	in := newInput()
	in.paste("first\nsecond")
	if in.text() != "first\nsecond" {
		t.Fatalf("text = %q", in.text())
	}
	if len(in.ed.lines) != 2 {
		t.Fatalf("lines = %d", len(in.ed.lines))
	}
}

// TestPasteFileStagesAttachment checks that pasting a path stages it: images ride
// inline as base64, other files keep their path.
func TestPasteFileStagesAttachment(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	in := newInput()
	in.paste(img + "\n")
	if len(in.attach) != 1 || in.attach[0].kind != "image" || in.attach[0].mime != "image/png" {
		t.Fatalf("image attachment = %+v", in.attach)
	}
	if in.text() != "" {
		t.Fatalf("path leaked into the text: %q", in.text())
	}
	blocks := in.blocks()
	if len(blocks) != 1 || blocks[0].Type != "image" || blocks[0].Name != "shot.png" || blocks[0].Data == "" {
		t.Fatalf("image blocks = %+v", blocks)
	}

	in.clear()
	in.paste(txt)
	if len(in.attach) != 1 || in.attach[0].kind != "file" || in.attach[0].path != txt {
		t.Fatalf("file attachment = %+v", in.attach)
	}
	if b := in.blocks(); len(b) != 1 || b[0].Type != "file" || b[0].Path != txt || b[0].Size != 5 {
		t.Fatalf("file blocks = %+v", b)
	}

	// Attachment chips ride above the text rows.
	in.ed.setText("look")
	rows, curRow, _ := in.rows(60)
	if len(rows) != 2 || !strings.Contains(rows[0], "notes.txt") || !strings.Contains(rows[1], "look") {
		t.Fatalf("rows = %q", rows)
	}
	if curRow != 1 {
		t.Fatalf("cursor row = %d, want 1", curRow)
	}
}

// TestInputBlocksOrder checks text comes first, then attachments, matching the
// WebUI's content array.
func TestInputBlocksOrder(t *testing.T) {
	in := newInput()
	in.ed.setText("hi")
	in.attach = []attachment{{kind: "file", name: "a.txt", path: "/tmp/a.txt", size: 3}}
	blocks := in.blocks()
	if len(blocks) != 2 || blocks[0].Type != "text" || blocks[0].Text != "hi" || blocks[1].Type != "file" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if in.empty() {
		t.Fatal("input with text reported empty")
	}
	in.clear()
	in.attach = []attachment{{kind: "file", name: "a.txt", path: "/tmp/a.txt"}}
	if in.empty() {
		t.Fatal("input with only an attachment reported empty")
	}
}

// TestDecodePaste checks a bracketed paste decodes to one press carrying the whole
// body, unchanged (newlines and all).
func TestDecodePaste(t *testing.T) {
	body := "line one\nline two\tindented"
	r := bufio.NewReader(strings.NewReader("\x1b[200~" + body + "\x1b[201~"))
	first, _, err := r.ReadRune()
	if err != nil {
		t.Fatal(err)
	}
	k := decodePress(r, first)
	if k.Kind != pressPaste || k.Text != body {
		t.Fatalf("paste = %+v, want %q", k, body)
	}
}

// TestDecodeNewlineKeys checks the three spellings of a newline key, and that a
// plain enter still sends.
func TestDecodeNewlineKeys(t *testing.T) {
	cases := []struct {
		seq  string
		want pressKind
	}{
		{"\x1b[13;2u", pressNewline},    // Kitty: shift+enter
		{"\x1b[27;2;13~", pressNewline}, // xterm modifyOtherKeys: shift+enter
		{"\x1b\r", pressNewline},        // alt+enter
		{"\n", pressNewline},            // ctrl+j
		{"\x1b[13u", pressEnter},        // Kitty: enter
		{"\r", pressEnter},              // legacy enter
	}
	for _, c := range cases {
		r := bufio.NewReader(strings.NewReader(c.seq))
		first, _, err := r.ReadRune()
		if err != nil {
			t.Fatalf("%q: %v", c.seq, err)
		}
		k := decodePress(r, first)
		if k.Kind != c.want {
			t.Fatalf("%q => kind %v, want %v", c.seq, k.Kind, c.want)
		}
	}
}

// TestDecodeKittyAndModifyOtherKeys pins that ordinary keys survive both
// protocols: ctrl+c stays a quit, plain letters stay text, backspace deletes.
func TestDecodeKittyAndModifyOtherKeys(t *testing.T) {
	for _, seq := range []string{"\x1b[99;5u", "\x1b[27;5;99~"} {
		r := bufio.NewReader(strings.NewReader(seq))
		first, _, _ := r.ReadRune()
		if k := decodePress(r, first); k.Kind != pressInterrupt {
			t.Fatalf("%q => %v, want interrupt", seq, k.Kind)
		}
	}
	r := bufio.NewReader(strings.NewReader("\x1b[65;2u"))
	first, _, _ := r.ReadRune()
	if k := decodePress(r, first); k.Kind != pressRune || k.R != 'A' {
		t.Fatalf("shift+a => %+v", k)
	}
	r = bufio.NewReader(strings.NewReader("\x1b[127u"))
	first, _, _ = r.ReadRune()
	if k := decodePress(r, first); k.Kind != pressBackspace {
		t.Fatalf("kitty backspace => %v", k.Kind)
	}
	r = bufio.NewReader(strings.NewReader("\x1b[27u"))
	first, _, _ = r.ReadRune()
	if k := decodePress(r, first); k.Kind != pressEsc {
		t.Fatalf("kitty escape => %v", k.Kind)
	}
}

// TestExistingPath pins the paste-path detector: one line, an existing regular
// file, home expanded, a trailing newline tolerated.
func TestExistingPath(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := existingPath(f + "\n"); got != f {
		t.Fatalf("existingPath = %q, want %q", got, f)
	}
	if got := existingPath(dir); got != "" {
		t.Fatalf("directory accepted: %q", got)
	}
	if got := existingPath("not a path\nsecond line"); got != "" {
		t.Fatalf("multi-line accepted: %q", got)
	}
	if got := existingPath("hello world"); got != "" {
		t.Fatalf("plain text accepted: %q", got)
	}
}
