package program

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

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
			return "", fmt.Errorf("%s: %s", target, r.err)
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
	name, _ := tc.Args["name"].(string)
	if name == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "name is required", tc.TraceID)
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

	go func() {
		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()

		src, err := w.backend.Read(ctx, name)
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("load %s: %v", name, err), tc.TraceID)
			return
		}

		sess, err := w.executor.Start(runCtx, src, args, w.callTool)
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("start: %v", err), tc.TraceID)
			return
		}
		// Drain the log stream so the sandbox never blocks on it; a future
		// pass surfaces these to the webui / caller.
		go func() { for range sess.Logs() {} }()

		result, err := sess.Result(runCtx)
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, fmt.Sprintf("run: %v", err), tc.TraceID)
			return
		}
		w.ReplyCompleted(tc.CallerID, tc.CallID, result, tc.TraceID)
	}()
}