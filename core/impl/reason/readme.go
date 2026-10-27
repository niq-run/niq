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
//   - worker.readme.update — replace the description. Advertised in the
//     peer-facing catalogue: the WebUI and authorized peers can discover it;
//     the bus ACL, not the announcement, decides who may actually send it.
//
// The readme is IDENTITY plus CURRENT FOCUS, not policy: who the worker is, what
// it is for, and what it has recently been working on. Rules, procedures and
// standing instructions belong to programs (instruction / playbook content on
// the program worker), so the descriptions say so — otherwise a worker drifts
// into writing its rulebook into the one free-text field it owns.
//
// The value is durable: an update signals the host to checkpoint, so the latest
// description survives a restart.
func (w *BaseReasonWorker) registerReadme() {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.RegisterReadme(
		"Read this worker's readme: a short, first-person statement of what it is responsible for "+
			"and what it has been working on lately. Use it to recall a worker's purpose and its current "+
			"focus before delegating to it, so the work you hand over lands where the worker already is. "+
			"It is not a rulebook: instructions, policies and step-by-step procedures live in programs.",
		w.ReadmeLocked, nil)

	w.Register(baseworker.Extension{
		Event: TypeReadmeUpdate,
		// Advertised: revising a description is a normal meta operation the WebUI
		// (and an authorized peer) may perform; the bus ACL decides who may send
		// the event.
		Response: baseworker.StringSchema("confirmation that the readme was replaced"),
		Description: "Replace this worker's readme. Write it first person and short, in two parts: " +
			"(1) what this worker is responsible for — its scope, what it is for; " +
			"(2) what it has recently been working on — its current focus, the last piece of work it " +
			"carried, and anything a peer handing it new work should know. " +
			"Refresh part (2) when the focus moves on to different work; change part (1) only when the " +
			"role or scope changes materially. " +
			"Rules, policies, standing instructions and step-by-step procedures do NOT belong here: " +
			"author them as programs (instruction / playbook) through the program worker's content tools " +
			"(program__write / program__edit / program__upsert). The readme answers \"what is this worker " +
			"and what is it doing right now\"; programs answer \"what must it do and how\".",
		Parameters: obj(map[string]any{
			"readme": map[string]any{"type": "string",
				"description": "New readme text: what this worker is responsible for, then what it has " +
					"recently been working on. An empty string clears it."},
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
