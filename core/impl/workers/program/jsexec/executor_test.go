package jsexec

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestExecutorRunsScriptUnderNode runs the real embedded launcher under node
// (bun may be absent) to exercise the whole local path: protocol, in-memory
// script compilation, a relayed call, and the final result.
func TestExecutorRunsScriptUnderNode(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	ex := New(Options{Args: []string{"node"}})

	src := `
		sdk.log("starting");
		const r = await sdk.call("ws", "bash", { cmd: "ls" });
		sdk.result("got:" + r);
	`
	var gotTarget, gotEvent string
	call := func(_ context.Context, target string, evt event.EventType, args map[string]any) (string, error) {
		gotTarget, gotEvent = target, string(evt)
		return "LS-OUT", nil
	}

	sess, err := ex.Start(context.Background(), src, map[string]any{"x": 1}, call)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Drain logs concurrently (never required for the result).
	gotLog := make(chan string, 8)
	go func() {
		for l := range sess.Logs() {
			gotLog <- l
		}
	}()

	res, err := sess.Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res != "got:LS-OUT" {
		t.Fatalf("result = %q, want got:LS-OUT", res)
	}
	if gotTarget != "ws" || gotEvent != "bash" {
		t.Fatalf("relayed call = %s/%s, want ws/bash", gotTarget, gotEvent)
	}
}

// TestExecutorSurfacesScriptError verifies a throwing script ends the session
// with an error rather than hanging.
func TestExecutorSurfacesScriptError(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	ex := New(Options{Args: []string{"node"}})
	sess, err := ex.Start(context.Background(), `throw new Error("boom");`, nil,
		func(context.Context, string, event.EventType, map[string]any) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := sess.Result(ctx); err == nil {
		t.Fatal("expected an error from a throwing script")
	}
}

// TestExecutorReportsProtocolStreamError verifies a protocol stream that ends
// without a terminal frame reports the scanner's own error (here: a line beyond
// the token limit) instead of a generic "exited without a result", so a broken
// launcher is diagnosable.
func TestExecutorReportsProtocolStreamError(t *testing.T) {
	// A launcher that writes one oversized line and exits: the scanner stops
	// with ErrTooLong before any JSON frame is complete.
	huge := "process.stdout.write('x'.repeat(9 * 1024 * 1024));"
	ex := New(Options{Args: []string{"node"}, Launcher: []byte(huge + "\n")})
	sess, err := ex.Start(context.Background(), `return 1`, nil,
		func(context.Context, string, event.EventType, map[string]any) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Cancel()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = sess.Result(ctx)
	if err == nil {
		t.Fatal("expected an error from an undecodable protocol stream")
	}
	if !strings.Contains(err.Error(), "protocol stream") {
		t.Fatalf("err = %v, want it to name the protocol stream", err)
	}
}
