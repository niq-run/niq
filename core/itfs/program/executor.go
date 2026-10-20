package program

import (
	"context"

	"github.com/niq-run/niq/core/itfs/event"
)

// ToolCaller performs a tool call on behalf of a running script: it turns the
// script's `call(target, event, arguments)` into a bus `request` to `target` and
// returns the reply VALUE — a string for a prose reply, the structured JSON value
// for a structured one (see event.ReplyResult for the same normalization applied
// to text). Returning the value, not its encoding, is what lets a script work
// with an object directly instead of parsing a string that may or may not be
// JSON. It must be safe for concurrent use — the program worker mints a distinct
// request id per call and awaits its reply, so a script may fire many calls in
// parallel.
//
// Only the trusted bus side (the program worker, which holds the WorkerSideChannel)
// implements it; the sandbox side just relays through this callback and never
// holds bus credentials or the channel.
type ToolCaller func(ctx context.Context, target string, eventType event.EventType, arguments map[string]any) (any, error)

// Executor runs a FormType:script program in an isolated sandbox. It is the
// execution half of a ProgramBackend — the counterpart of Store. Implementations
// are interchangeable: a local subprocess (bun/node, containing accidental
// misuse), a real sandbox such as the planned QuickJS one, or a remote sandbox
// gateway, without the program worker or the SDK changing (this is what makes
// the execution layer replaceable).
//
// Scripts only ever reach the world through ToolCaller; they cannot touch the
// bus or the filesystem. Each Start returns a ScriptSession that streams the
// script's log lines and yields its final sdk.result.
type Executor interface {
	// Start begins one script session without blocking on its completion.
	// src is the script program's code; args are the program.run arguments; call
	// is the ToolCaller that the script's call() is relayed through. Start owns
	// the sandbox process lifecycle: Cancel on the returned session must tear it
	// down, and the session must clean up its own resources on Result/error.
	Start(ctx context.Context, src string, args map[string]any, call ToolCaller) (ScriptSession, error)
}

// ScriptSession is the program worker's handle to one running script.
type ScriptSession interface {
	// Logs streams the lines the script emitted via sdk.log. It is closed when
	// the session ends. The program worker drains it asynchronously (e.g. for
	// webui visibility); it need not be fully read for caching.
	Logs() <-chan string

	// Result blocks until the script calls sdk.result / returns (yielding that
	// VALUE, type preserved: a string stays a string, an object stays an object)
	// or ctx is done (returning ctx.Err()). After it returns, the session's
	// resources and process are already released.
	Result(ctx context.Context) (any, error)

	// Cancel terminates the session: kills the sandbox process and releases
	// resources.
	Cancel()
}
