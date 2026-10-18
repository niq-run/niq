package program

import (
	"context"
	"strconv"
	"strings"
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

// peerReady puts a worker on the program worker's roster the way the bus does
// (a worker.ready delivery), so a script's call to it is not rejected as
// unknown.
func peerReady(w *Worker, id string) {
	w.process(context.Background(), event.New(event.TypeWorkerReady, id, map[string]any{}))
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
	peerReady(w, "ws")

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

// TestReadmeExposed verifies the worker answers a worker.readme call with its
// self-description guide.
func TestReadmeExposed(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(t.TempDir())})

	evt := event.New(event.TypeWorkerReadme, "asker", map[string]any{})
	evt.RequestId = "r1"
	w.DispatchExtension(evt)

	waitUntil(t, time.Second, func() bool {
		for _, e := range ch.snapshot() {
			if e.Type == event.TypeRequestCompleted && e.RequestId == "r1" {
				r, _ := e.Payload["result"].(string)
				return strings.Contains(r, "Program worker") && strings.Contains(r, "run")
			}
		}
		return false
	}, "worker.readme reply")
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
	peerReady(w, "ws")

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

// scriptedSession emits a script's log lines and then a result, so the reply
// path can be asserted end to end.
type scriptedSession struct {
	lines  []string
	result string
	logs   chan string
}

func (s *scriptedSession) Logs() <-chan string { return s.logs }
func (s *scriptedSession) Result(ctx context.Context) (string, error) {
	for _, l := range s.lines {
		s.logs <- l
	}
	close(s.logs)
	return s.result, nil
}
func (s *scriptedSession) Cancel() {}

type scriptedExecutor struct{ sess *scriptedSession }

func (e scriptedExecutor) Start(context.Context, string, map[string]any, program.ToolCaller) (program.ScriptSession, error) {
	e.sess.logs = make(chan string)
	return e.sess, nil
}

// replyResult returns the result text of the first request.completed/failed for
// the given call id.
func replyText(t *testing.T, ch *stubChannel, callID string) (string, bool) {
	t.Helper()
	for _, e := range ch.snapshot() {
		if e.RequestId != callID {
			continue
		}
		if e.Type == event.TypeRequestCompleted || e.Type == event.TypeRequestFailed {
			r, _ := e.Payload["result"].(string)
			return r, e.Type == event.TypeRequestCompleted
		}
	}
	return "", false
}

// TestHandleRunReturnsScriptLog verifies a script's sdk.log lines come back to
// the CALLER inside the run reply (not just to the worker's own log): the
// result stays first and the log follows under its own header.
func TestHandleRunReturnsScriptLog(t *testing.T) {
	ch := &stubChannel{}
	sess := &scriptedSession{lines: []string{"step 1", "step 2"}, result: "THE-RESULT"}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(t.TempDir()), Executor: scriptedExecutor{sess: sess}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "coder", CallID: "call-log", TraceID: "tr",
		Args: map[string]any{"source": "sdk.log('x'); return 1"},
	})

	waitUntil(t, 3*time.Second, func() bool {
		_, ok := replyText(t, ch, "call-log")
		return ok
	}, "program.run reply")

	got, _ := replyText(t, ch, "call-log")
	if !strings.HasPrefix(got, "THE-RESULT") {
		t.Fatalf("reply must lead with the result, got %q", got)
	}
	if !strings.Contains(got, "--- script log ---") || !strings.Contains(got, "step 1") || !strings.Contains(got, "step 2") {
		t.Fatalf("reply must carry the script log, got %q", got)
	}
}

// TestHandleRunOmitsEmptyScriptLog verifies a silent run replies with exactly
// its result (no empty log section).
func TestHandleRunOmitsEmptyScriptLog(t *testing.T) {
	ch := &stubChannel{}
	sess := &scriptedSession{result: "ONLY-RESULT"}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(t.TempDir()), Executor: scriptedExecutor{sess: sess}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "coder", CallID: "call-quiet", TraceID: "tr",
		Args: map[string]any{"source": "return 'ONLY-RESULT'"},
	})

	waitUntil(t, 3*time.Second, func() bool {
		_, ok := replyText(t, ch, "call-quiet")
		return ok
	}, "program.run reply")
	if got, _ := replyText(t, ch, "call-quiet"); got != "ONLY-RESULT" {
		t.Fatalf("quiet run reply = %q, want exactly the result", got)
	}
}

// TestScriptLogCapsTail verifies the retained window is the END of the run and
// that dropped lines are reported.
func TestScriptLogCapsTail(t *testing.T) {
	var l scriptLog
	for i := 0; i < scriptLogMaxLines+5; i++ {
		l.add("line " + strconv.Itoa(i))
	}
	text := l.text()
	if !strings.Contains(text, "[5 earlier line(s) dropped]") {
		t.Fatalf("want a dropped-lines note, got %q", text[:60])
	}
	if !strings.Contains(text, "line "+strconv.Itoa(scriptLogMaxLines+4)) {
		t.Fatal("want the tail of the log retained")
	}
	if strings.Contains(text, "line 0\n") {
		t.Fatal("head of the log should have been dropped")
	}
}

// TestCallToolUnknownWorkerFailsFast verifies a call to a worker that is not on
// the bus is rejected immediately instead of waiting for a reply that can never
// arrive (which used to burn the whole run timeout, or hang the caller's round).
func TestCallToolUnknownWorkerFailsFast(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(t.TempDir())})
	peerReady(w, "ws")
	// Keep the discovery round out of the timing: no peer answers, so the call
	// must give up after rosterDiscoverWait at the latest.
	start := time.Now()
	_, err := w.callTool(context.Background(), "no_such_worker", "bash", map[string]any{})
	elapsed := time.Since(start)
	if err == nil || !strings.Contains(err.Error(), "unknown worker: no_such_worker") {
		t.Fatalf("err = %v, want unknown worker", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("unknown target took %s, want a fast failure", elapsed)
	}
}

// TestRosterTracksPresence verifies ready/gone maintain the roster and that a
// worker that goes away is treated as unknown again.
func TestRosterTracksPresence(t *testing.T) {
	w := New(Config{ID: "program", Bus: &stubChannel{}, Backend: pgbackend.New(t.TempDir())})
	if !w.isKnown("program") {
		t.Fatal("the worker must know itself")
	}
	peerReady(w, "ws")
	if !w.isKnown("ws") {
		t.Fatal("worker.ready must add the peer")
	}
	w.process(context.Background(), event.New(event.TypeWorkerGone, "ws", map[string]any{}))
	if w.isKnown("ws") {
		t.Fatal("worker.gone must remove the peer")
	}
}

// TestHandleRunAcceptsPath verifies the unified address spelling: path runs a
// stored program (name stays accepted as the legacy alias).
func TestHandleRunAcceptsPath(t *testing.T) {
	ch := &stubChannel{}
	root := t.TempDir()
	writeProgramAs(t, root, "ptc-path", "ptc-path", "script", "stored script", "return 1")
	sess := &scriptedSession{result: "VIA-PATH"}
	w := New(Config{ID: "program", Bus: ch, Backend: pgbackend.New(root), Executor: scriptedExecutor{sess: sess}})

	go w.handleRun(context.Background(), baseworker.ToolCall{
		CallerID: "coder", CallID: "call-path", TraceID: "tr",
		Args: map[string]any{"path": "ptc-path"},
	})
	waitUntil(t, 3*time.Second, func() bool {
		_, ok := replyText(t, ch, "call-path")
		return ok
	}, "program.run reply")
	if got, _ := replyText(t, ch, "call-path"); !strings.HasPrefix(got, "VIA-PATH") {
		t.Fatalf("reply = %q, want the program's result", got)
	}
}

// TestCapScriptResultKeepsHeadAndTail verifies the result cap elides the middle
// and says how much it dropped.
func TestCapScriptResultKeepsHeadAndTail(t *testing.T) {
	big := strings.Repeat("A", scriptResultMaxBytes) + strings.Repeat("B", scriptResultMaxBytes)
	got := capScriptResult(big)
	if len(got) > scriptResultMaxBytes+200 {
		t.Fatalf("capped length %d exceeds the cap by too much", len(got))
	}
	if !strings.HasPrefix(got, "A") || !strings.HasSuffix(got, "B") {
		t.Fatal("cap must keep the head and the tail")
	}
	if !strings.Contains(got, "bytes elided") {
		t.Fatal("cap must report the elided amount")
	}
	if small := capScriptResult("short"); small != "short" {
		t.Fatalf("small result must pass through, got %q", small)
	}
}
