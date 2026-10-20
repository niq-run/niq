package program

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/impl/workers/directory"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/program"
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
		ch <- callResult{text: replyText(evt.Payload)}
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

// replyText renders a request.completed payload as the text a script receives.
//
// A text reply travels as {"result": "<text>"}; a capability that answers with
// STRUCTURED content (workspace.readImg answers {"content":[{type:"image",…}]}
// so a reason worker can decode it into an image block) sends its payload as-is,
// with no result field. A script cannot render an image, but it must not receive
// an empty string either: hand it the payload as JSON so nothing is lost
// silently.
func replyText(payload map[string]any) string {
	if _, ok := payload["result"]; ok {
		return event.ReplyResult(payload)
	}
	// No "result": a content-block reply (ReplyPayload, e.g. workspace.readImg).
	// A script cannot render an image, but it must not receive an empty string
	// either — hand it the payload as JSON so nothing disappears silently.
	if b, err := json.Marshal(payload); err == nil {
		return string(b)
	}
	return ""
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
	if w.backend == nil {
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

		sess, err := w.backend.Start(runCtx, src, args, w.callTool)
		if errors.Is(err, program.ErrNoExecutor) {
			// A store-only environment: the worker manages content but cannot run
			// a script. Say that, rather than surfacing a start failure.
			w.ReplyFailed(tc.CallerID, tc.CallID, "executor not configured", tc.TraceID)
			return
		}
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

// ── reply shaping ──
//
// What goes back to the caller: the result, plus (when the script logged) a
// "--- script log ---" section. Kept next to the run that produces it: this is
// the only caller, and a reader tracing program.run should not have to open a
// second file to see what the reply ends up looking like.

// Script-log return policy. A run's sdk.log lines are returned to the CALLER
// alongside the result, because for a PTC round the caller is the model: seeing
// its own script's log is what lets it debug a failing run inside the same
// round instead of asking for a second one. The text is capped — the tail is
// what explains a failure, and an unbounded log would blow up the reply payload
// and the caller's context.
const (
	scriptLogMaxLines = 100
	scriptLogMaxBytes = 8 * 1024

	// scriptResultMaxBytes caps the RESULT text handed back to the caller, in the
	// same spirit (and roughly the same size) as the workspace tools' 20KB
	// output cap: a run that returns a huge blob must not silently blow up the
	// caller's context. Head and tail are kept, the middle is elided, so both
	// the beginning and the ending of the payload stay readable.
	scriptResultMaxBytes = 20 * 1024
)

// scriptLog collects a run's sdk.log lines for the reply. It is written by the
// session's log-draining goroutine and read after the session ends, so access
// is guarded.
type scriptLog struct {
	mu      sync.Mutex
	lines   []string
	bytes   int
	dropped int
}

// add appends one log line, dropping the OLDEST lines once the caps are hit so
// the retained window is the end of the run.
func (l *scriptLog) add(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line = strings.TrimRight(line, "\n")
	l.lines = append(l.lines, line)
	l.bytes += len(line) + 1
	for len(l.lines) > scriptLogMaxLines || (l.bytes > scriptLogMaxBytes && len(l.lines) > 1) {
		l.bytes -= len(l.lines[0]) + 1
		l.lines = l.lines[1:]
		l.dropped++
	}
}

// text renders the collected lines as the reply's log section, or "" when the
// script logged nothing (so a quiet run's reply is exactly its result).
func (l *scriptLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	var b strings.Builder
	if l.dropped > 0 {
		// Written in pieces: one WriteString per piece, no throwaway
		// concatenation (a linter flags the concatenated form).
		b.WriteString("[")
		b.WriteString(strconv.Itoa(l.dropped))
		b.WriteString(" earlier line(s) dropped]\n")
	}
	b.WriteString(strings.Join(l.lines, "\n"))
	return b.String()
}

// withScriptLog appends the run's log section to the reply text. The result
// stays first and untouched, so a caller that only reads the head is unaffected.
func withScriptLog(result string, logs *scriptLog) string {
	text := logs.text()
	if text == "" {
		return result
	}
	const header = "\n\n--- script log ---\n"
	if result == "" {
		return strings.TrimPrefix(header, "\n\n") + text
	}
	return result + header + text
}

// capScriptResult keeps a result within scriptResultMaxBytes by eliding its
// middle. The note states how much was dropped, so the caller can tell a
// truncated payload from a short one.
func capScriptResult(result string) string {
	if len(result) <= scriptResultMaxBytes {
		return result
	}
	head := scriptResultMaxBytes / 2
	tail := scriptResultMaxBytes - head
	// Cut on rune boundaries so the kept text stays valid UTF-8.
	headEnd := head
	for headEnd > 0 && !utf8.RuneStart(result[headEnd]) {
		headEnd--
	}
	tailStart := len(result) - tail
	for tailStart < len(result) && !utf8.RuneStart(result[tailStart]) {
		tailStart++
	}
	elided := tailStart - headEnd
	return result[:headEnd] +
		"\n\n[... " + strconv.Itoa(elided) + " bytes elided ...]\n\n" +
		result[tailStart:]
}
