// Package quickjsexec implements the program worker's script execution with
// an in-process QuickJS VM (modernc.org/quickjs — the engine compiled to Go, no
// cgo). It is the execution half of a ProgramBackend, a sibling of pgbackend
// for the same reason jsexec was: mechanism stays in the worker, the
// environment lives in its own package behind program.Executor.
//
// What this backend buys over a node subprocess:
//
//   - The global surface is ours. There is no `process`, `require`, `fetch` or
//     dynamic `import`, so the sandbox contract in the README stops being a
//     promise the script is asked to keep and becomes a fact about the VM.
//   - Resources have hard ceilings. SetMemoryLimit bounds a runaway allocation,
//     SetMaxStackSize turns deep recursion into a catchable error, and
//     Interrupt() from a watchdog goroutine stops a pure CPU loop.
//   - A run costs ~0.2 ms and ~0.2 MB instead of a 37 ms / 43 MB process.
//
// The trade-off is that the VM lives in this process: a crash in the engine is
// a crash in niq, and the memory of concurrent runs shares one heap. Ownership
// discipline (see session.go) and the per-run memory limit are what keep that
// bounded. The Executor seam remains so a process or remote backend can take
// this place.
//
// This is still an EXECUTION environment, not a security boundary against a
// deliberate attacker: the engine is a large C program in the same address
// space. It is a much stronger accident boundary than `node -e`, because a
// script can no longer reach the host at all — only the host functions we
// register are visible to it.
package quickjsexec

import (
	"context"
	_ "embed"

	"github.com/niq-run/niq/core/itfs/program"
)

// prelude is evaluated before every script in the same VM: it builds the sdk
// facade and the host calls it relays through.
//
//go:embed prelude.js
var prelude string

const (
	// defaultMemoryLimit bounds one run's JS heap. Scripts orchestrate tool
	// calls; they do not hold data, so a few MB is the normal high-water mark.
	defaultMemoryLimit = 64 << 20
	// defaultMaxStackSize bounds JS call depth, so runaway recursion surfaces
	// as a catchable "stack overflow" instead of a Go stack overflow.
	defaultMaxStackSize = 1 << 20
)

// Options configures the executor. The zero value is the defaults.
type Options struct {
	// MemoryLimit is one session's JS heap ceiling in bytes.
	MemoryLimit uintptr
	// MaxStackSize is the JS call-depth budget in bytes.
	MaxStackSize uintptr
}

// Executor runs script programs in a fresh QuickJS VM per session.
type Executor struct {
	opts Options
}

// Executor is the execution half of a ProgramBackend.
var _ program.Executor = (*Executor)(nil)

// New builds the executor with the given options (zero Options = the defaults).
func New(opts Options) *Executor {
	if opts.MemoryLimit == 0 {
		opts.MemoryLimit = defaultMemoryLimit
	}
	if opts.MaxStackSize == 0 {
		opts.MaxStackSize = defaultMaxStackSize
	}
	return &Executor{opts: opts}
}

// Start begins one script session on its own goroutine and returns immediately.
// The VM is created there rather than here, because a QuickJS VM may only be
// touched by the goroutine that created it (program.Executor's contract allows
// a session to report its failure through Result).
func (e *Executor) Start(ctx context.Context, src string, args map[string]any, call program.ToolCaller, msg program.MessageSender) (program.ScriptSession, error) {
	s := newSession(ctx, e.opts, src, args, call, msg)
	go s.run()
	return s, nil
}
