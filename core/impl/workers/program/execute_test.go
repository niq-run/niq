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
	src    string
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

type replayExecutor struct{ session *replaySession }

func (e *replayExecutor) Start(_ context.Context, src string, args map[string]any, call program.ToolCaller) (program.ScriptSession, error) {
	e.session.src = src
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
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(root), Executor: &replayExecutor{session: sess}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "caller", CallID: "c1", Name: "run",
		Args: map[string]any{"name": "hello"}, TraceID: "t",
	})

	// Executor received the script source.
	waitUntil(t, 2*time.Second, func() bool { return sess.src != "" }, "script source passed to executor")

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
