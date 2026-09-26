package wsbackend

import (
	"bytes"
	"context"
	"errors"
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

		// Symlink escape is deliberately permitted: a symlinked entry whose
		// logical path is still inside the mount is followed, even when the
		// target is outside. This is what lets a directory be symlinked into
		// the workspace (e.g. a program directory) and still be read.
		// Syntactic traversal ("..") above is still rejected.
		{
			name: "symlink out of mount is followed",
			raw:  "link-out/passwd",
			prep: func() {
				os.Symlink("/etc", filepath.Join(root, "link-out"))
			},
			wantErr: false,
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
			b := NewEmbeddedBackend([]string{root})
			_, err := b.resolve(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolve(%q) error = %v, wantErr = %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}

// TestEscapeErrorType verifies escapes surface as *EscapeError so callers can
// distinguish a boundary violation from an ordinary I/O failure.
func TestEscapeErrorType(t *testing.T) {
	b := NewEmbeddedBackend([]string{t.TempDir()})
	_, err := b.resolve("/etc/passwd")
	var esc *EscapeError
	if !errors.As(err, &esc) {
		t.Fatalf("err = %v (%T), want *EscapeError", err, err)
	}
	if esc.Path != "/etc/passwd" {
		t.Fatalf("EscapeError.Path = %q", esc.Path)
	}
}

// TestMultiMountResolve verifies multi-mount path resolution: relative paths
// resolve against the primary (first) mount, absolute paths are accepted when
// they fall inside any mount, and everything else is rejected.
func TestMultiMountResolve(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	primaryAbs, _ := filepath.Abs(primary)
	secondaryAbs, _ := filepath.Abs(secondary)

	b := NewEmbeddedBackend([]string{primary, secondary})

	// Touch a file in each mount so symlink resolution has real targets.
	if err := os.WriteFile(filepath.Join(primaryAbs, "p.txt"), []byte("p"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondaryAbs, "s.txt"), []byte("s"), 0644); err != nil {
		t.Fatal(err)
	}

	// Relative → primary.
	got, err := b.resolve("p.txt")
	if err != nil || got != filepath.Join(primaryAbs, "p.txt") {
		t.Fatalf("relative resolve = %q, %v", got, err)
	}

	// Absolute inside second mount → allowed.
	got, err = b.resolve(filepath.Join(secondaryAbs, "s.txt"))
	if err != nil || got != filepath.Join(secondaryAbs, "s.txt") {
		t.Fatalf("second-mount resolve = %q, %v", got, err)
	}

	// Absolute outside all mounts → rejected.
	if _, err := b.resolve("/etc/passwd"); err == nil {
		t.Fatalf("outside path must be rejected")
	}

	// Sibling-directory prefix pitfall: /tmp/proj vs /tmp/project2.
	sibling := secondaryAbs + "2"
	if sibling == secondaryAbs {
		t.Skip("sibling path collision")
	}
	if err := os.MkdirAll(sibling, 0755); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sibling)
	if _, err := b.resolve(filepath.Join(sibling, "x")); err == nil {
		t.Fatalf("sibling prefix path must be rejected")
	}
}

// TestAddMountValidation covers the AddMount validation rules.
func TestAddMountValidation(t *testing.T) {
	primary := t.TempDir()
	b := NewEmbeddedBackend([]string{primary})

	// Nonexistent path accepted as a phantom entry (approval grants scope,
	// not existence).
	phantom := filepath.Join(primary, "future")
	if _, err := b.AddMount(phantom); err != nil {
		t.Fatalf("phantom mount rejected: %v", err)
	}
	// File (non-dir) rejected.
	file := filepath.Join(primary, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.AddMount(file); err == nil {
		t.Fatalf("file mount must be rejected")
	}
	// Duplicate real path rejected; canonical path returned on success.
	nested := filepath.Join(primary, "sub")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	added, err := b.AddMount(nested)
	if err != nil {
		t.Fatalf("AddMount: %v", err)
	}
	if added != nested {
		t.Fatalf("AddMount = %q, want %q", added, nested)
	}
	if _, err := b.AddMount(nested + string(filepath.Separator)); err == nil {
		t.Fatalf("duplicate mount must be rejected")
	}
	if got := b.Mounts(); len(got) != 3 || got[0] != primary || got[1] != phantom || got[2] != nested {
		t.Fatalf("Mounts = %v", got)
	}

	// A phantom mount's children resolve once the directory appears.
	if err := os.MkdirAll(filepath.Join(phantom, "created"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := b.resolve(filepath.Join(phantom, "created", "x")); err != nil {
		t.Fatalf("child of created phantom mount: %v", err)
	}

	// RemoveMount detaches by path; the last remaining mount is protected.
	removed, err := b.RemoveMount(phantom)
	if err != nil || !removed {
		t.Fatalf("RemoveMount = %v, %v", removed, err)
	}
	if got := b.Mounts(); len(got) != 2 {
		t.Fatalf("Mounts after remove = %v", got)
	}
	if _, err := b.RemoveMount(phantom); err == nil {
		t.Fatalf("removing an unmounted path must fail")
	}
}

// TestReplaceMountsLenient verifies a wholesale replacement accepting
// not-yet-existing paths (persisted mounts survive directories that were
// removed while the worker was down).
func TestReplaceMountsLenient(t *testing.T) {
	primary := t.TempDir()
	b := NewEmbeddedBackend([]string{primary})
	missing := filepath.Join(primary, "gone")
	if err := b.ReplaceMounts([]string{primary, missing}); err != nil {
		t.Fatalf("ReplaceMounts with missing dir: %v", err)
	}
	if got := b.Mounts(); len(got) != 2 || got[1] != missing {
		t.Fatalf("Mounts = %v", got)
	}
	// Empty replacement still rejected.
	if err := b.ReplaceMounts(nil); err == nil {
		t.Fatalf("empty ReplaceMounts must be rejected")
	}
}

// TestMultiMountBashDefaultCwd verifies bash runs in the primary mount by
// default and can address the second mount by absolute path.
func TestMultiMountBashDefaultCwd(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	b := NewEmbeddedBackend([]string{primary, secondary})

	r, err := b.Bash(context.Background(), "pwd", "", BashLimits{MaxBytes: 1024})
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !strings.Contains(r.Stdout, filepath.Base(primary)) {
		t.Fatalf("default cwd = %q, want primary %q", r.Stdout, primary)
	}

	r, err = b.Bash(context.Background(), "pwd", secondary, BashLimits{MaxBytes: 1024})
	if err != nil {
		t.Fatalf("Bash(secondary cwd): %v", err)
	}
	if !strings.Contains(r.Stdout, filepath.Base(secondary)) {
		t.Fatalf("cwd = %q, want secondary %q", r.Stdout, secondary)
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
