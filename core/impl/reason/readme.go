package reason

import (
	"strings"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// TypeReadmeUpdate is the write half of a reason worker's self-description: it
// replaces the readme (worker.readme is the read half). The worker owns this
// text — it is seeded from params at creation and then evolves with the work, so
// a later recall (list/search, then read the readme) describes what the worker
// actually became, not what it was spawned as.
const TypeReadmeUpdate event.EventType = "worker.readme.update"

// registerReadme registers both halves of the self-description:
//
//   - worker.readme — the generic read capability (baseworker), so peers, the
//     WebUI and the worker itself can recall what it is for.
//   - worker.readme.update — replace the description. SelfOnly: it is not part
//     of the peer-facing contract, but a directed request still reaches it (the
//     bus ACL, not the announcement, gates who may update).
//
// The value is durable: an update signals the host to checkpoint, so the latest
// description survives a restart.
func (w *BaseReasonWorker) registerReadme() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.RegisterReadme(
		"Read this worker's readme: a short description of what it is for. "+
			"Use it to recall a worker's purpose before delegating to it.",
		w.ReadmeLocked, nil)

	w.Register(baseworker.Extension{
		Event: TypeReadmeUpdate,
		// SelfOnly: updating your own description is a self-management act, not a
		// capability peers should see in the roster. A directed request from
		// another authorized worker still dispatches here.
		SelfOnly: true,
		Response: baseworker.StringSchema("confirmation that the readme was replaced"),
		Description: "Replace this worker's readme — the short self-description of what it is for. " +
			"Write it when the worker's role or scope changes, so later recall " +
			"(list/search, then read the readme) finds an accurate description.",
		Parameters: obj(map[string]any{
			"readme": map[string]any{"type": "string",
				"description": "New description text. An empty string clears it."},
		}),
	}, w.handleReadmeUpdate)
}

// Readme returns the worker's current self-description.
func (w *BaseReasonWorker) Readme() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.readme
}

// ReadmeLocked is Readme without taking w.mu. Extension handlers must use it:
// they are invoked from process(), which holds w.mu for the whole dispatch, and
// sync.Mutex is not reentrant — locking here would self-deadlock the event loop
// (the worker then never processes another event, so it stops reasoning for
// good).
func (w *BaseReasonWorker) ReadmeLocked() string { return w.readme }

// handleReadmeUpdate replaces the worker's readme and makes it durable.
//
// Runs inside process() with w.mu held (see ReadmeLocked), so it writes the
// field directly instead of taking the lock.
func (w *BaseReasonWorker) handleReadmeUpdate(evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	if tc.CallID == "" {
		return
	}
	text, ok := tc.Args["readme"].(string)
	if !ok {
		w.ReplyFailed(tc.CallerID, tc.CallID, "readme must be a string", tc.TraceID)
		return
	}
	w.readme = strings.TrimSpace(text)
	w.NotifyDurableChange()
	w.ReplyCompleted(tc.CallerID, tc.CallID, "readme updated", tc.TraceID)
}
