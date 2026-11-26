package tui

import (
	"bufio"
	"context"
	"os"
	"testing"
	"time"

	"github.com/niq-run/niq/app/client/tui/stdin"
)

// TestReadKeysStopsWithoutAKey pins the exit path: cancelling and bumping the
// deadline must end the reader with no keystroke behind it, which is what makes a
// single ctrl+c leave instead of waiting for a second press or a mouse click.
func TestReadKeysStopsWithoutAKey(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	src := stdin.NewReader(pr)
	in := bufio.NewReader(src)
	out := make(chan press, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		readKeysFrom(ctx, in, src.SetReadDeadline, out)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond) // it is now blocked in the read
	cancel()
	_ = src.SetReadDeadline(time.Now())

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the reader did not stop without a keystroke")
	}
}

// TestReadKeysDeliversInterrupt pins the whole path: a ctrl+c byte written to the
// pumped stdin becomes a pressInterrupt on the key channel, in one press.
func TestReadKeysDeliversInterrupt(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	src := stdin.NewReader(pr)
	in := bufio.NewReader(src)
	out := make(chan press, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go readKeysFrom(ctx, in, src.SetReadDeadline, out)

	if _, err := pw.Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-out:
		if p.Kind != pressInterrupt {
			t.Fatalf("ctrl+c decoded as %v, want pressInterrupt", p.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("ctrl+c did not reach the key channel")
	}
}
