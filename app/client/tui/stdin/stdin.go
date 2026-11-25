// Package stdin pumps the terminal's standard input into a buffered reader with
// working read deadlines.
//
// Go's read deadlines do not work on stdin on macOS: SetReadDeadline returns
// "file type does not support deadline". Every timeout in the key decoder — the
// idle wake-up, the 40 ms "lone Esc" window, the cancellation handshake —
// silently did nothing, so a reader blocked in a read could only be woken by the
// next keystroke. So stdin is pumped into a buffered channel by one goroutine for
// the life of the process, and deadlines are implemented in userspace on top of
// it: a read waits on bytes, a timer, or a deadline change, all of which work
// here.
//
// The implementation is pure Go with no platform-specific path — no syscall, no
// build tags — so it behaves the same on Linux, macOS and Windows.
package stdin

import (
	"bufio"
	"io"
	"os"
	"sync"
	"time"
)

// Reader adapts a blocking file to io.Reader with working deadlines.
type Reader struct {
	ch   chan byte
	mu   sync.Mutex
	dl   time.Time     // current read deadline
	wake chan struct{} // closed whenever the deadline changes
}

// NewReader starts a goroutine that reads f and serves its bytes through a
// deadline-aware Reader.
func NewReader(f *os.File) *Reader {
	r := &Reader{ch: make(chan byte, 4096), wake: make(chan struct{})}
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := f.Read(buf)
			for i := 0; i < n; i++ {
				r.ch <- buf[i]
			}
			if err != nil {
				close(r.ch)
				return
			}
		}
	}()
	return r
}

// SetReadDeadline records when the next read should give up. A change also wakes
// a read that is already waiting, so cancellation is immediate.
func (r *Reader) SetReadDeadline(t time.Time) error {
	r.mu.Lock()
	r.dl = t
	close(r.wake)
	r.wake = make(chan struct{})
	r.mu.Unlock()
	return nil
}

// Read waits for at least one byte, honouring the deadline, then returns whatever
// else is already buffered. Errors are os.ErrDeadlineExceeded (retryable) or EOF.
func (r *Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		r.mu.Lock()
		dl, wake := r.dl, r.wake
		r.mu.Unlock()

		var timer *time.Timer
		var expired <-chan time.Time
		if !dl.IsZero() {
			d := time.Until(dl)
			if d <= 0 {
				return 0, os.ErrDeadlineExceeded
			}
			timer = time.NewTimer(d)
			expired = timer.C
		}

		select {
		case b, ok := <-r.ch:
			stopTimer(timer)
			if !ok {
				return 0, io.EOF
			}
			p[0] = b
			n := 1
			for n < len(p) {
				select {
				case b, ok := <-r.ch:
					if !ok {
						return n, nil
					}
					p[n] = b
					n++
				default:
					return n, nil
				}
			}
			return n, nil
		case <-expired:
			return 0, os.ErrDeadlineExceeded
		case <-wake:
			stopTimer(timer)
			// The deadline changed; recompute it.
		}
	}
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

var (
	once sync.Once
	src  *Reader
	buf  *bufio.Reader
)

// setDeadline is the deadline setter for the shared stream; a no-op until Stream
// first opens it.
var setDeadline = func(time.Time) error { return nil }

// SetDeadline bounds a pending read on the shared stream. It is a no-op until
// Stream is first called, so decoding tests over an in-memory reader are
// unaffected.
func SetDeadline(t time.Time) error { return setDeadline(t) }

// Stream is the one buffered reader over the terminal, shared by the picker and
// the chat. One pump goroutine reads stdin for the life of the process, so a
// session can never leave a blocked reader behind to swallow the next view's
// keys.
func Stream() *bufio.Reader {
	once.Do(func() {
		src = NewReader(os.Stdin)
		buf = bufio.NewReaderSize(src, 1024)
		setDeadline = src.SetReadDeadline
	})
	return buf
}
