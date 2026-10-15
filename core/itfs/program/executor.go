package program

import (
	"context"

	"github.com/niq-run/niq/core/itfs/event"
)

// ToolCaller performs a tool call on behalf of a running script: it turns the
// script's `call(target, event, arguments)` into a bus `request` to `target`
// and returns the request.completed / request.failed text. It must be safe for
// concurrent use — the program worker mints a distinct request id per call and
// awaits its reply, so a script may fire many calls in parallel.
//
// Only the trusted bus side (the program worker, which holds the WorkerSideChannel)
// implements it; the sandbox side just relays through this callback and never
// holds bus credentials or the channel.
type ToolCaller func(ctx context.Context, target string, eventType event.EventType, arguments map[string]any) (string, error)

// ScriptProgramExecutor runs a FormType:script program in an isolated sandbox.
// It is the program worker's EXECUTION backend — the counterpart of the storage
// Backend. Implementations are interchangeable: local (Bun --sandbox), a future
// local sandbox, or a remote sandbox gateway, without the program worker or the
// SDK changing (this is what makes the execution layer replaceable).
//
// Scripts only ever reach the world through ToolCaller; they cannot touch the
// bus or the filesystem. Each Start returns a ScriptSession that streams the
// script's log lines and yields its final sdk.result.
type ScriptProgramExecutor interface {
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

	// Result blocks until the script calls sdk.result (returning its payload) or
	// ctx is done (returning ctx.Err()). After it returns, the session's
	// resources and process are already released.
	Result(ctx context.Context) (string, error)

	// Cancel terminates the session: kills the sandbox process and releases
	// resources.
	Cancel()
}