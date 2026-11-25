package stdin

import (
	"errors"
	"os"
	"testing"
	"time"
)

// TestReaderDeadline pins the userspace deadlines: a read with no data must time
// out (macOS cannot set a deadline on a terminal's stdin), and changing the
// deadline must wake a read that is already waiting.
func TestReaderDeadline(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()
	r := NewReader(pr)

	if err := r.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var b [1]byte
	if _, err := r.Read(b[:]); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read = %v, want deadline exceeded", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("deadline took %v", d)
	}

	// A deadline change wakes a blocked read so shutdown does not wait for a key.
	_ = r.SetReadDeadline(time.Now().Add(time.Hour))
	done := make(chan error, 1)
	go func() {
		_, err := r.Read(b[:])
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = r.SetReadDeadline(time.Now())
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("woken read = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("changing the deadline did not wake the read")
	}

	// Bytes still flow through.
	_ = r.SetReadDeadline(time.Time{})
	if _, err := pw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	n, err := r.Read(b[:])
	if err != nil || n != 1 || b[0] != 'x' {
		t.Fatalf("read data: n=%d err=%v b=%q", n, err, b[0])
	}
}
