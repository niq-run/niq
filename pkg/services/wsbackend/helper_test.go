package wsbackend

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	root, _ := filepath.Abs(dir)

	tests := []struct {
		name    string
		raw     string
		prep    func()
		wantErr bool
	}{
		// valid relative
		{name: "plain relative", raw: "main.go", wantErr: false},
		{name: "dot prefix", raw: "./main.go", wantErr: false},
		{name: "nested relative", raw: "a/b/c.go", wantErr: false},
		{name: "double slash", raw: "a//b.go", wantErr: false},

		// escape attempts
		{name: "parent escape", raw: "../etc/passwd", wantErr: true},
		{name: "deep parent escape", raw: "../../etc/passwd", wantErr: true},

		// absolute
		{name: "absolute inside", raw: filepath.Join(root, "main.go"), wantErr: false},
		{name: "absolute outside", raw: "/etc/passwd", wantErr: true},

		// empty
		{name: "empty", raw: "", wantErr: true},

		// symlink escape
		{
			name: "symlink escape to /etc",
			raw:  "link-out/passwd",
			prep: func() {
				os.Symlink("/etc", filepath.Join(root, "link-out"))
			},
			wantErr: true,
		},

		// ~ expansion — always outside a temp workspace
		{name: "tilde with slash", raw: "~/file.go", wantErr: true},
		{name: "tilde alone", raw: "~", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prep != nil {
				tt.prep()
			}
			_, err := resolvePath(root, tt.raw)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolvePath(%q) error = %v, wantErr = %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

func TestFormatBash(t *testing.T) {
	got := FormatBash(BashResult{Stdout: "hello\nworld", ExitCode: 0})
	want := "Exit code: 0\nSTDOUT:\nhello\nworld"
	if got != want {
		t.Fatalf("FormatBash = %q, want %q", got, want)
	}
}

func TestFormatBashTruncated(t *testing.T) {
	got := FormatBash(BashResult{
		Stdout:     "head...tail",
		Stderr:     "err",
		ExitCode:   0,
		Truncated:  true,
		TotalBytes: 20480,
	})
	for _, want := range []string{
		"Exit code: 0",
		"STDOUT:\nhead...tail",
		"STDERR:\nerr",
		"[Output truncated: total 20480 bytes.",
		"Use head/tail/grep/filter to narrow output, or redirect to file then use read_file.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("FormatBash missing %q:\n%s", want, got)
		}
	}
}

func TestBoundedWriterUnderLimit(t *testing.T) {
	w := newBoundedWriter(BashLimits{MaxBytes: 1024})
	data := []byte("line1\nline2\nline3\n")
	w.Write(data)
	if got := w.String(); got != string(data) {
		t.Fatalf("under-limit output must be reconstructed exactly, got %q", got)
	}
	if w.total.Load() != int64(len(data)) || w.lines.Load() != 3 {
		t.Fatalf("totals = %d bytes, %d lines", w.total.Load(), w.lines.Load())
	}
}

func TestBoundedWriterOverLimit(t *testing.T) {
	// MaxBytes=100 → head 25 + tail 75; 200 'a's exceed the budget.
	w := newBoundedWriter(BashLimits{MaxBytes: 100})
	payload := bytes.Repeat([]byte("a"), 200)
	w.Write(payload)
	s := w.String()
	if !strings.Contains(s, "bytes omitted") {
		t.Fatalf("over-limit output must mark the omitted middle: %q", s)
	}
	if w.total.Load() != 200 {
		t.Fatalf("total = %d, want 200", w.total.Load())
	}
	if !strings.HasPrefix(s, strings.Repeat("a", 25)) {
		t.Fatalf("head missing: %q", s)
	}
	if !strings.HasSuffix(s, strings.Repeat("a", 75)) {
		t.Fatalf("tail missing: %q", s)
	}
}

func TestBoundedWriterUnlimited(t *testing.T) {
	w := newBoundedWriter(BashLimits{})
	payload := bytes.Repeat([]byte("x"), 5000)
	w.Write(payload)
	if got := w.String(); got != string(payload) {
		t.Fatalf("unlimited writer must keep everything, got %d bytes", len(got))
	}
}

func TestForwardLines(t *testing.T) {
	var got []string
	var pending []byte
	forwardLines(&pending, []byte("ab\ncd"), func(s string) { got = append(got, s) })
	forwardLines(&pending, []byte("\nef\n"), func(s string) { got = append(got, s) })
	want := []string{"ab", "cd", "ef"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
