package program

import (
	"context"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/workers/program/pgbackend"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/program"
)

// snapshot returns a copy of the stub channel's sent events under its lock
// (safe to call while the worker goroutine is also writing).
func (c *stubChannel) snapshot() []event.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]event.Event(nil), c.sent...)
}

func waitUntil(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}

// TestCallToolAwaitsReply verifies the core relay: callTool emits a request
// event with a minted id, and resolveReply wakes it with the reply text.
func TestCallToolAwaitsReply(t *testing.T) {
	ch := &stubChannel{}
	root := t.TempDir()
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(root)})

	out := make(chan string, 1)
	go func() {
		text, err := w.callTool(context.Background(), "ws", "bash", map[string]any{"cmd": "ls"})
		if err != nil {
			out <- "ERR:" + err.Error()
			return
		}
		out <- text
	}()

	var callID string
	waitUntil(t, 2*time.Second, func() bool {
		for _, e := range ch.snapshot() {
			if string(e.Type) == "bash" && e.RequestId != "" {
				callID = e.RequestId
				return true
			}
		}
		return false
	}, "bash request sent")

	reply := event.New(event.TypeRequestCompleted, "ws", map[string]any{"result": "LS-OUT"})
	reply.RequestId = callID
	w.resolveReply(reply)

	select {
	case r := <-out:
		if r != "LS-OUT" {
			t.Fatalf("callTool returned %q, want LS-OUT", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callTool did not return after reply")
	}
}

// replaySession runs the received ToolCaller once and returns it composed into
// its final result, proving the handleRun → executor → callTool → resolveReply
// round-trip sustains a real tool call.
type replaySession struct {
	call   program.ToolCaller
	result string
}

func (s *replaySession) Logs() <-chan string {
	c := make(chan string)
	close(c)
	return c
}
func (s *replaySession) Result(ctx context.Context) (string, error) {
	text, err := s.call(ctx, "ws", "bash", map[string]any{"cmd": "ls"})
	if err != nil {
		return "ERR:" + err.Error(), nil
	}
	return s.result + ":" + text, nil
}
func (s *replaySession) Cancel() {}

type replayExecutor struct {
	session *replaySession
	gotSrc  chan string // optional: receives the script source (test observation)
}

// blockingSession blocks in Result until its context is done, to exercise the
// per-run timeout (the worker must cancel and reply failed, not hang).
type blockingSession struct{}

func (blockingSession) Logs() <-chan string { c := make(chan string); close(c); return c }
func (blockingSession) Result(ctx context.Context) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (blockingSession) Cancel() {}

type blockingExecutor struct{}

func (blockingExecutor) Start(context.Context, string, map[string]any, program.ToolCaller) (program.ScriptSession, error) {
	return blockingSession{}, nil
}

// TestHandleRunInlineSource verifies mode B: an inline source runs without any
// stored program.
func TestHandleRunInlineSource(t *testing.T) {
	ch := &stubChannel{}
	sess := &replaySession{result: "INLINE-DONE"}
	gotSrc := make(chan string, 1)
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(t.TempDir()), Executor: &replayExecutor{session: sess, gotSrc: gotSrc}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "caller", CallID: "c2", Name: "run",
		Args: map[string]any{"source": "sdk.result('x')"}, TraceID: "t",
	})

	select {
	case s := <-gotSrc:
		if s != "sdk.result('x')" {
			t.Fatalf("executor got %q, want the inline source", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executor not started for inline source")
	}
}

// TestHandleRunRejectsBothModes verifies exactly one of name / source is allowed.
func TestHandleRunRejectsBothModes(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(t.TempDir()), Executor: blockingExecutor{}})

	w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "caller", CallID: "c3", Name: "run",
		Args: map[string]any{"name": "a", "source": "b"}, TraceID: "t",
	})
	waitUntil(t, time.Second, func() bool {
		for _, e := range ch.snapshot() {
			if e.Type == event.TypeRequestFailed && e.RequestId == "c3" {
				return true
			}
		}
		return false
	}, "both-modes rejection")
}

// TestHandleRunTimesOut verifies a session that never returns is killed by the
// per-run timeout and the worker replies failed instead of hanging.
func TestHandleRunTimesOut(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "hang", "hang", "demo", "// never returns")
	ch := &stubChannel{}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(root), Executor: blockingExecutor{}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "caller", CallID: "c9", Name: "run",
		Args: map[string]any{"name": "hang", "timeout_seconds": 0.05}, TraceID: "t",
	})

	waitUntil(t, 3*time.Second, func() bool {
		for _, e := range ch.snapshot() {
			if e.Type == event.TypeRequestFailed && e.RequestId == "c9" {
				return true
			}
		}
		return false
	}, "timeout reply")
}

func (e *replayExecutor) Start(_ context.Context, src string, args map[string]any, call program.ToolCaller) (program.ScriptSession, error) {
	if e.gotSrc != nil {
		e.gotSrc <- src
	}
	e.session.call = call
	return e.session, nil
}

// TestHandleRunRelaysAndReplies drives the whole path: program.run loads the
// script, the executor's session calls a tool through the ToolCaller (which
// becomes a request event), the reply is resolved, and the worker replies with
// the composed result.
func TestHandleRunRelaysAndReplies(t *testing.T) {
	root := t.TempDir()
	writeProgram(t, root, "hello", "hello", "demo", "// script source placeholder")

	ch := &stubChannel{}
	sess := &replaySession{result: "DONE"}
	gotSrc := make(chan string, 1)
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(root), Executor: &replayExecutor{session: sess, gotSrc: gotSrc}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "caller", CallID: "c1", Name: "run",
		Args: map[string]any{"name": "hello"}, TraceID: "t",
	})

	// Executor received the script source.
	select {
	case s := <-gotSrc:
		if s == "" {
			t.Fatal("executor got empty script source")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executor not started")
	}

	// The session's tool call goes out as a bash request.
	var callID string
	waitUntil(t, 2*time.Second, func() bool {
		for _, e := range ch.snapshot() {
			if string(e.Type) == "bash" && e.RequestId != "" {
				callID = e.RequestId
				return true
			}
		}
		return false
	}, "bash request sent")

	reply := event.New(event.TypeRequestCompleted, "ws", map[string]any{"result": "LS-OUT"})
	reply.RequestId = callID
	w.resolveReply(reply)

	// The worker replies to program.run with the composed result.
	waitUntil(t, 2*time.Second, func() bool {
		for _, e := range ch.snapshot() {
			if e.Type == event.TypeRequestCompleted && e.RequestId == "c1" {
				if r, _ := e.Payload["result"].(string); r == "DONE:LS-OUT" {
					return true
				}
			}
		}
		return false
	}, "program.run reply")
}
