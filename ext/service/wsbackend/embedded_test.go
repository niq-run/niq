package wsbackend

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBashUnderLimit(t *testing.T) {
	b := NewEmbeddedBackend(t.TempDir())
	r, err := b.Bash(context.Background(), "printf 'hello\nworld\n'", "", BashLimits{MaxBytes: 1024, MaxLines: 100})
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if r.Truncated {
		t.Fatalf("small output must not be truncated: %+v", r)
	}
	if !strings.Contains(r.Stdout, "hello") || !strings.Contains(r.Stdout, "world") {
		t.Fatalf("stdout = %q", r.Stdout)
	}
	if r.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", r.ExitCode)
	}
}

func TestBashTruncatesOversizedOutput(t *testing.T) {
	b := NewEmbeddedBackend(t.TempDir())
	r, err := b.Bash(context.Background(), "seq 1 100000", "", BashLimits{MaxBytes: 1024, MaxLines: 100000})
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !r.Truncated {
		t.Fatalf("oversized output must be truncated")
	}
	if r.TotalBytes <= 1024 {
		t.Fatalf("TotalBytes = %d, want > 1024", r.TotalBytes)
	}
	if !strings.Contains(r.Stdout, "1\n") {
		t.Fatalf("head (start of output) missing: %q", r.Stdout)
	}
	if !strings.Contains(r.Stdout, "bytes omitted") {
		t.Fatalf("omission marker missing: %q", r.Stdout)
	}
}

func TestBashKillsOnOverflow(t *testing.T) {
	b := NewEmbeddedBackend(t.TempDir())
	start := time.Now()
	r, err := b.Bash(context.Background(), "while true; do echo tick; done", "", BashLimits{MaxBytes: 256, MaxLines: 100000})
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !r.Truncated {
		t.Fatalf("runaway output must be truncated")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("overflow kill took too long: %v", time.Since(start))
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	b := NewEmbeddedBackend(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := b.Bash(ctx, "sleep 30", "", BashLimits{})
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("timeout kill took too long: %v", time.Since(start))
	}
}

// TestBashKillsBackgroundChild verifies the process-group kill reaches a
// backgrounded grandchild: a command that spawns a background sleep and then
// waits must not leave the backgrounded process alive after the deadline.
func TestBashKillsBackgroundChild(t *testing.T) {
	b := NewEmbeddedBackend(t.TempDir())
	marker := filepath.Join(t.TempDir(), "bg.pid")
	cmd := fmt.Sprintf("sleep 60 & echo $! > '%s'; sleep 60", marker)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := b.Bash(ctx, cmd, "", BashLimits{}); err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}

	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read bg pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse bg pid %q: %v", pidBytes, err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return // ESRCH: the backgrounded child is gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("backgrounded child %d survived the group kill", pid)
}

func TestBashStreamForwardsLines(t *testing.T) {
	b := NewEmbeddedBackend(t.TempDir())
	var mu sync.Mutex
	var lines []string
	r, err := b.BashStream(context.Background(), "printf 'a\nb\nc\n'", "",
		func(s string) {
			mu.Lock()
			lines = append(lines, s)
			mu.Unlock()
		}, BashLimits{MaxBytes: 1024, MaxLines: 100})
	if err != nil {
		t.Fatalf("BashStream: %v", err)
	}
	if r.Truncated || !strings.Contains(r.Stdout, "a\nb\nc") {
		t.Fatalf("result = %+v", r)
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"a", "b", "c"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("streamed lines missing %q: %v", want, lines)
		}
	}
}
