package program

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/workers/directory"
	"github.com/niq-run/niq/core/itfs/event"
)

// defaultRunTimeout bounds one script-program session; a runaway (or unfaithful
// LLM) script is killed rather than running forever. Overridable per call via
// the timeout_seconds argument.
const defaultRunTimeout = 120 * time.Second

// callResult is the outcome of a forwarded tool call: either the
// request.completed text or the request.failed / rejected reason.
type callResult struct {
	text string
	err  string
}

func newCallID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// callTool implements the ToolCaller contract: it turns a script's call into a
// bus `request` event to `target` and blocks until the request.completed /
// failed / rejected reply (matched by a minted request id). Concurrent-safe —
// each call gets its own pending slot, so a script may fire many calls in
// parallel await their results independently.
func (w *Worker) callTool(ctx context.Context, target string, eventType event.EventType, arguments map[string]any) (string, error) {
	// Fail fast on a target no one can answer: the send would be routed nowhere,
	// no reply could ever resolve this call, and the script (and the caller's
	// round) would hang until the run timeout. A typo'd or not-yet-started
	// worker is the common case, so it deserves an immediate, precise error.
	if err := w.checkTarget(ctx, target); err != nil {
		return "", err
	}
	return w.relay(ctx, target, eventType, arguments)
}

// relay performs one request/reply round trip, WITHOUT validating the target.
// callTool is relay plus the target check; the directory probe uses relay
// directly so it cannot recurse into itself.
func (w *Worker) relay(ctx context.Context, target string, eventType event.EventType, arguments map[string]any) (string, error) {
	callID := newCallID()
	ch := make(chan callResult, 1)

	w.callMu.Lock()
	if w.pending == nil {
		w.pending = make(map[string]chan callResult)
	}
	w.pending[callID] = ch
	w.callMu.Unlock()
	defer func() {
		w.callMu.Lock()
		delete(w.pending, callID)
		w.callMu.Unlock()
	}()

	evt := event.New(eventType, w.ID(), arguments)
	evt.RequestId = callID
	if err := w.Channel.Send(ctx, evt, target); err != nil {
		return "", fmt.Errorf("call %s/%s: %w", target, eventType, err)
	}

	select {
	case r := <-ch:
		if r.err != "" {
			// Capability-level failure reported by the peer: a typed error, so
			// checkTarget can tell "answered no" from "never answered".
			return "", &remoteError{target: target, reason: r.err}
		}
		return r.text, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// resolveReply routes a request.completed / failed / rejected event to the
// script call that issued it, waking its blocked callTool. It returns true if
// the event matched a pending call (and so should not be dispatched as a
// worker extension).
func (w *Worker) resolveReply(evt event.Event) bool {
	w.callMu.Lock()
	ch := w.pending[evt.RequestId]
	if ch != nil {
		delete(w.pending, evt.RequestId)
	}
	w.callMu.Unlock()
	if ch == nil {
		return false
	}

	switch evt.Type {
	case event.TypeRequestCompleted:
		text, _ := evt.Payload["result"].(string)
		ch <- callResult{text: text}
	case event.TypeRequestFailed:
		msg, _ := evt.Payload["error"].(string)
		ch <- callResult{err: msg}
	case event.TypeRequestRejected:
		reason, _ := evt.Payload["reason"].(string)
		ch <- callResult{err: "rejected: " + reason}
	default:
		return false
	}
	return true
}

// handleRun serves the program.run tool: it resolves the script source from
// the backend, hands it to the executor, and relays every script tool call
// through callTool. It runs on its own goroutine so the worker's event loop
// stays free to receive the request.* replies those calls await.
func (w *Worker) handleRun(ctx context.Context, tc baseworker.ToolCall) {
	// path is the unified address vocabulary (load/edit/write/search/delete all
	// take path); name is the pre-accessor spelling, still accepted.
	name, _ := tc.Args["path"].(string)
	if name == "" {
		name, _ = tc.Args["name"].(string)
	}
	source, _ := tc.Args["source"].(string)

	// Two modes: run a stored script program by name (reuse), or run inline
	// source directly (ad-hoc / transient, nothing persisted). Exactly one.
	switch {
	case name == "" && source == "":
		w.ReplyFailed(tc.CallerID, tc.CallID, "provide either path (a stored script program) or source (inline script)", tc.TraceID)
		return
	case name != "" && source != "":
		w.ReplyFailed(tc.CallerID, tc.CallID, "provide only one of path or source, not both", tc.TraceID)
		return
	}
	if w.executor == nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, "executor not configured", tc.TraceID)
		return
	}
	args, _ := tc.Args["args"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}

	timeout := defaultRunTimeout
	if v, ok := tc.Args["timeout_seconds"].(float64); ok && v > 0 {
		timeout = time.Duration(v) * time.Second
	}

	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		src := source
		if src == "" {
			loaded, err := w.backend.Read(ctx, name)
			if err != nil {
				w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("load %s: %v", name, err), tc.TraceID)
				return
			}
			src = loaded
		}

		sess, err := w.executor.Start(runCtx, src, args, w.callTool)
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("start: %v", err), tc.TraceID)
			return
		}
		// Drain the script's sdk.log lines: mirrored into this worker's log (so a
		// run is observable server-side) AND collected for the reply, so the
		// caller sees them without a second round. The session closes the stream
		// when it ends, so the drain finishes on its own.
		logs := &scriptLog{}
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for line := range sess.Logs() {
				logs.add(line)
				log.Printf("[program %s] script: %s", w.ID(), line)
			}
		}()

		// Result returns when the script finishes or the session ctx is done
		// (timeout / cancel); the executor tears the sandbox down either way.
		result, err := sess.Result(runCtx)
		// Give the drain a moment to consume the tail; the channel is already
		// closed by now, so this only covers the goroutine's hand-off.
		select {
		case <-drained:
		case <-time.After(500 * time.Millisecond):
		}
		if err != nil {
			sess.Cancel()
			w.ReplyFailed(tc.CallerID, tc.CallID, withScriptLog(fmt.Sprintf("run: %v", err), logs), tc.TraceID)
			return
		}
		w.ReplyCompleted(tc.CallerID, tc.CallID, withScriptLog(capScriptResult(result), logs), tc.TraceID)
	}()
}

// ── target validation (via the directory worker) ──

// directoryProbeWait bounds the roster probe. In-process it answers in
// microseconds; the bound only matters when the directory is slow or absent.
const directoryProbeWait = time.Second

// probeBackoffWindow is how long a failed probe suppresses further probes, so a
// project without a directory worker does not pay the probe timeout on every
// script call.
const probeBackoffWindow = 30 * time.Second

// remoteError is a capability-level failure reported by a peer
// (request.failed / rejected), as opposed to a transport or timeout problem.
// It lets callers tell "the peer answered: no" from "the peer did not answer".
type remoteError struct {
	target string
	reason string
}

func (e *remoteError) Error() string { return e.target + ": " + e.reason }

// checkTarget rejects a call to a worker the fleet does not know.
//
// The roster is NOT tracked here: the directory worker already owns it (it
// collects worker.ready / worker.gone for the whole fleet), so the program
// worker asks it on demand instead of keeping a second copy that could drift.
// Only a definitive "no such worker" fails the call — an unreachable directory
// means "cannot tell", which must not block a call that may well work.
func (w *Worker) checkTarget(ctx context.Context, target string) error {
	// An empty target cannot be routed anywhere; say so instead of sending a
	// request that would wait for the run timeout.
	if target == "" {
		return fmt.Errorf("call requires a target worker id")
	}
	if target == w.ID() || target == w.directoryID || w.directoryID == "" {
		return nil
	}
	// A recent probe came back indeterminate: skip probing for a while.
	w.mu.Lock()
	backedOff := time.Now().Before(w.probeBackoff)
	w.mu.Unlock()
	if backedOff {
		return nil
	}

	probeCtx, cancel := context.WithTimeout(ctx, directoryProbeWait)
	defer cancel()
	_, err := w.relay(probeCtx, w.directoryID, directory.TypeGetWorkerInfo, map[string]any{"worker": target})
	if err == nil {
		return nil
	}
	var re *remoteError
	if errors.As(err, &re) {
		// The directory answered: it does not know this worker.
		return fmt.Errorf("unknown worker: %s", target)
	}
	// Timeout / transport / ctx cancelled: we cannot tell. Remember it so the
	// next calls are not delayed, and let them through — the run timeout still
	// bounds a genuinely dead target.
	w.mu.Lock()
	w.probeBackoff = time.Now().Add(probeBackoffWindow)
	w.mu.Unlock()
	log.Printf("[program %s] directory probe for %q indeterminate (%v) - target checks paused for %s",
		w.ID(), target, err, probeBackoffWindow)
	return nil
}
