package quickjsexec

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// run starts a session and waits for its result, draining logs in the
// background the way the worker does.
func run(t *testing.T, ex *Executor, src string, args map[string]any, call func(context.Context, string, event.EventType, map[string]any) (any, error), msg func(context.Context, string, string) error) (any, error, []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sess, err := ex.Start(ctx, src, args, call, msg)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Cancel()

	var mu sync.Mutex
	var logs []string
	go func() {
		for line := range sess.Logs() {
			mu.Lock()
			logs = append(logs, line)
			mu.Unlock()
		}
	}()

	value, err := sess.Result(ctx)
	mu.Lock()
	defer mu.Unlock()
	return value, err, append([]string(nil), logs...)
}

func noCall(context.Context, string, event.EventType, map[string]any) (any, error) {
	return "", nil
}

// TestRunsScript exercises the whole path: the SDK facade, a relayed call, a
// fire-and-forget message, logs, and the final result.
func TestRunsScript(t *testing.T) {
	ex := New(Options{})

	var mu sync.Mutex
	var gotTarget, gotEvent, gotMsgTarget, gotMsgText string
	call := func(_ context.Context, target string, evt event.EventType, _ map[string]any) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		gotTarget, gotEvent = target, string(evt)
		return "LS-OUT", nil
	}
	msg := func(_ context.Context, target, text string) error {
		mu.Lock()
		defer mu.Unlock()
		gotMsgTarget, gotMsgText = target, text
		return nil
	}

	src := `
		sdk.log("starting");
		const r = await sdk.call("ws", "bash", { cmd: "ls" });
		await sdk.message("peer", "hello");
		sdk.result("got:" + r);
	`
	value, err, logs := run(t, ex, src, nil, call, msg)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if value != "got:LS-OUT" {
		t.Fatalf("result = %#v, want got:LS-OUT", value)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotTarget != "ws" || gotEvent != "bash" {
		t.Fatalf("relayed call = %s/%s, want ws/bash", gotTarget, gotEvent)
	}
	if gotMsgTarget != "peer" || gotMsgText != "hello" {
		t.Fatalf("relayed message = %s/%q, want peer/hello", gotMsgTarget, gotMsgText)
	}
	if len(logs) != 1 || logs[0] != "starting" {
		t.Fatalf("logs = %q, want [starting]", logs)
	}
}

// TestCallResultKeepsType verifies a structured reply reaches the script as a
// real object/array, so a script reads r.mounts directly instead of parsing a
// string it had to guess about.
func TestCallResultKeepsType(t *testing.T) {
	ex := New(Options{})
	src := `
		const listing = await sdk.call("directory", "search", {});
		const text = await sdk.call("ws", "bash", { cmd: "ls" });
		sdk.result([
			typeof listing,
			Array.isArray(listing) ? listing[0].worker_id : null,
			typeof text,
			text,
		].join("|"));
	`
	call := func(_ context.Context, target string, _ event.EventType, _ map[string]any) (any, error) {
		if target == "directory" {
			return []any{map[string]any{"worker_id": "workspace"}}, nil
		}
		return "LS-OUT", nil
	}
	value, err, _ := run(t, ex, src, nil, call, nil)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if value != "object|workspace|string|LS-OUT" {
		t.Fatalf("result = %q, want object|workspace|string|LS-OUT", value)
	}
}

// TestResultPrecedence pins the contract shared with the node launcher:
// sdk.result beats a top-level return, the last call wins, and a script that
// produces neither yields "".
func TestResultPrecedence(t *testing.T) {
	ex := New(Options{})
	for _, tc := range []struct {
		name string
		src  string
		want any
	}{
		{"result", `sdk.result("A"); sdk.result("B");`, "B"},
		{"return", `return 41`, float64(41)},
		{"result over return", `sdk.result("R"); return "ignored";`, "R"},
		{"neither", `const x = 1;`, ""},
		{"object", `return { a: [1, "x"], b: null }`, map[string]any{"a": []any{float64(1), "x"}, "b": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err, _ := run(t, ex, tc.src, nil, noCall, nil)
			if err != nil {
				t.Fatalf("result: %v", err)
			}
			if !sameValue(value, tc.want) {
				t.Fatalf("result = %#v, want %#v", value, tc.want)
			}
		})
	}
}

// sameValue compares the JSON-ish values a run can produce.
func sameValue(got, want any) bool {
	g, gok := got.(map[string]any)
	w, wok := want.(map[string]any)
	if gok != wok {
		return false
	}
	if gok {
		if len(g) != len(w) {
			return false
		}
		for k := range w {
			if !sameValue(g[k], w[k]) {
				return false
			}
		}
		return true
	}
	gs, gok := got.([]any)
	ws, wok := want.([]any)
	if gok != wok {
		return false
	}
	if gok {
		if len(gs) != len(ws) {
			return false
		}
		for i := range ws {
			if !sameValue(gs[i], ws[i]) {
				return false
			}
		}
		return true
	}
	return got == want
}

// TestArgsInjected verifies program.run's args arrive as sdk.args.
func TestArgsInjected(t *testing.T) {
	ex := New(Options{})
	value, err, _ := run(t, ex, `sdk.result(sdk.args.cmd + ":" + sdk.args.n)`, map[string]any{"cmd": "ls", "n": 3}, noCall, nil)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if value != "ls:3" {
		t.Fatalf("result = %#v, want ls:3", value)
	}
}

// TestScriptErrorNamesScriptLine verifies a runtime error points at the user's
// own source line, not at the prelude.
func TestScriptErrorNamesScriptLine(t *testing.T) {
	ex := New(Options{})
	src := "const a = 1;\nnull.boom;\n"
	_, err, _ := run(t, ex, src, nil, noCall, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "script line 2") {
		t.Fatalf("err = %q, want it to name script line 2", msg)
	}
	if !strings.Contains(msg, "null.boom") {
		t.Fatalf("err = %q, want it to quote the failing source line", msg)
	}
}

// TestSyntaxErrorNamesScriptLine verifies a parse error is located too.
func TestSyntaxErrorNamesScriptLine(t *testing.T) {
	ex := New(Options{})
	_, err, _ := run(t, ex, "const ok = 1;\nconst bad = ;\n", nil, noCall, nil)
	if err == nil {
		t.Fatal("expected a syntax error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "SyntaxError") || !strings.Contains(msg, "script line 2") {
		t.Fatalf("err = %q, want a SyntaxError naming script line 2", msg)
	}
}

// TestCallsFanOut verifies Promise.all really overlaps: three 100 ms calls must
// finish in ~100 ms, not 300 ms. This is the reason the session drives promises
// instead of blocking inside sdk.call.
func TestCallsFanOut(t *testing.T) {
	ex := New(Options{})
	call := func(context.Context, string, event.EventType, map[string]any) (any, error) {
		time.Sleep(100 * time.Millisecond)
		return "ok", nil
	}
	start := time.Now()
	value, err, _ := run(t, ex, `
		const t0 = Date.now();
		await Promise.all([sdk.call("a", "e", {}), sdk.call("b", "e", {}), sdk.call("c", "e", {})]);
		sdk.result(Date.now() - t0);
	`, nil, call, nil)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if elapsed, _ := value.(float64); elapsed > 200 {
		t.Fatalf("elapsed = %v ms, want the calls to overlap (< 200 ms)", elapsed)
	}
	if total := time.Since(start); total > 5*time.Second {
		t.Fatalf("run took %v", total)
	}
}

// TestRejectedCallThrows verifies a capability-level failure becomes a catchable
// exception in the script, not a silent string.
func TestRejectedCallThrows(t *testing.T) {
	ex := New(Options{})
	call := func(context.Context, string, event.EventType, map[string]any) (any, error) {
		return nil, errors.New("target refused")
	}
	value, err, _ := run(t, ex, `
		try { await sdk.call("x", "e", {}); sdk.result("no") }
		catch (e) { sdk.result("caught:" + e.message) }
	`, nil, call, nil)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if value != "caught:target refused" {
		t.Fatalf("result = %#v, want caught:target refused", value)
	}
}

// TestConsoleIsShimmed verifies console.log works even though the engine has no
// console, and that its lines land in the run's log.
func TestConsoleIsShimmed(t *testing.T) {
	ex := New(Options{})
	_, err, logs := run(t, ex, `console.log("hello", { a: 1 }); sdk.result(1)`, nil, noCall, nil)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "hello") || !strings.Contains(logs[0], `{"a":1}`) {
		t.Fatalf("logs = %q, want one line containing hello and {\"a\":1}", logs)
	}
}

// TestCancelStopsRun verifies Cancel ends a session that is waiting on a call.
func TestCancelStopsRun(t *testing.T) {
	ex := New(Options{})
	ctx := context.Background()
	blocked := make(chan struct{})
	call := func(c context.Context, _ string, _ event.EventType, _ map[string]any) (any, error) {
		close(blocked)
		<-c.Done()
		return nil, c.Err()
	}
	sess, err := ex.Start(ctx, `await sdk.call("slow", "e", {}); sdk.result("never")`, nil, call, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		t.Fatal("call was not relayed")
	}
	sess.Cancel()

	resCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.Result(resCtx); err == nil {
		t.Fatal("expected an error from a cancelled session")
	}
}

// TestCpuLoopIsInterrupted verifies a script that never yields cannot outlive
// its deadline: Interrupt from the watchdog is the only way out.
func TestCpuLoopIsInterrupted(t *testing.T) {
	ex := New(Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	sess, err := ex.Start(ctx, `while (true) {}`, nil, noCall, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	_, err = sess.Result(ctx)
	if err == nil {
		t.Fatal("expected the run to be interrupted")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("interrupt took %v", elapsed)
	}
}

// TestMemoryLimitStopsAllocationBomb verifies the per-run heap ceiling is real:
// a script allocating without bound fails instead of growing the process.
func TestMemoryLimitStopsAllocationBomb(t *testing.T) {
	ex := New(Options{MemoryLimit: 16 << 20})
	_, err, _ := run(t, ex, `const a = []; for (;;) a.push(new Array(1000).fill("xxxx"));`, nil, noCall, nil)
	if err == nil {
		t.Fatal("expected the allocation bomb to fail")
	}
	if !strings.Contains(err.Error(), "memory") && !strings.Contains(err.Error(), "out of memory") {
		t.Fatalf("err = %q, want an out-of-memory failure", err)
	}
}

// TestStalledScriptFailsFast verifies a script awaiting a promise that can never
// settle (and with no call in flight) fails immediately instead of hanging until
// the run timeout.
func TestStalledScriptFailsFast(t *testing.T) {
	ex := New(Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, err := ex.Start(ctx, `await new Promise(() => {});`, nil, noCall, nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	start := time.Now()
	if _, err := sess.Result(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stalled script took %v to fail", elapsed)
	}
}

// TestMessageWithoutSenderFails verifies a missing MessageSender rejects the
// script's message() instead of panicking.
func TestMessageWithoutSenderFails(t *testing.T) {
	ex := New(Options{})
	value, err, _ := run(t, ex, `
		try { await sdk.message("peer", "hi"); sdk.result("no") }
		catch (e) { sdk.result("caught:" + e.message) }
	`, nil, noCall, nil)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if !strings.Contains(value.(string), "caught:") {
		t.Fatalf("result = %#v, want the message to reject", value)
	}
}
