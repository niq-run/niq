// Package jsexec implements the program worker's local script-program execution
// backend: it spawns a JavaScript runtime (node by default, or whatever
// Options.Args names) running an embedded, trusted launcher, and speaks a
// JSON-Lines protocol with it over stdio. The launcher compiles the script in
// memory and injects a minimal SDK; every SDK call() is relayed back here and
// turned into a bus request.
//
// It sits next to pgbackend for the same reason: both are swappable
// implementations of one program-worker interface (execution there, storage
// here), so the worker keeps its mechanism and the environment-specific code
// stays in its own package. A QuickJS sandbox (planned) becomes a sibling
// implementation of the same program.Executor.
//
// It is one implementation of program.Executor (the execution half of a
// ProgramBackend). A remote sandbox (self-hosted or a provider) can be dropped
// in behind the same interface.
//
// Trust model: this is an EXECUTION environment, not a security sandbox. It is
// built to contain ACCIDENTAL misuse — a stray import (static import syntax does
// not even parse inside new Function), a wrong file, a runaway loop — not a
// deliberate attack. The process is a normal bun/node process, so a runtime
// `await import("node:fs")` or `fetch` is reachable, and the script runs as the
// same OS user as niq. That is a deliberate trade-off: scripts should be as
// usable as local tools, and the input is the local model's own code.
//
// A real isolation boundary is a different design and belongs to a different
// Executor: a QuickJS sandbox (planned), a wrapper command via Options.Args
// (sandbox-exec / bwrap / container), or a remote execution backend. The seam
// exists so that swap costs nothing in the worker, the SDK or the scripts.
package jsexec

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/program"
)

//go:embed launcher.mjs
var defaultLauncher []byte

// Options configures the subprocess executor.
type Options struct {
	// Args is the command prefix the launcher path is appended to. Defaults to
	// the node binary (present on the vast majority of hosts); override it with
	// script_executor_args, e.g. ["bun", "run"] or a sandbox wrapper
	// (sandbox-exec / bwrap / a container) — see the package comment.
	Args []string
	// Launcher overrides the embedded launcher source. Injectable for tests.
	Launcher []byte
}

// Executor is the local subprocess execution backend.
type Executor struct {
	args     []string
	launcher []byte
}

// Executor is the execution half of a ProgramBackend.
var _ program.Executor = (*Executor)(nil)

// New builds the executor with the given options (zero Options = the defaults).
func New(opts Options) *Executor {
	args := opts.Args
	if args == nil {
		// node is the baseline runtime: it is what the launcher protocol is
		// written against, and it is present on nearly every host. bun works
		// equally well via Options.Args, and a QuickJS-based Executor will
		// replace this subprocess implementation entirely.
		args = []string{"node"}
	}
	launcher := opts.Launcher
	if launcher == nil {
		launcher = defaultLauncher
	}
	return &Executor{args: args, launcher: launcher}
}

// Start materializes the launcher, spawns it, and sends the run message. The
// session owns the process; its Cancel kills it.
func (e *Executor) Start(ctx context.Context, src string, args map[string]any, call program.ToolCaller) (program.ScriptSession, error) {
	path, err := e.materialize()
	if err != nil {
		return nil, err
	}

	argv := append(append([]string{}, e.args[1:]...), path)
	cmd := exec.Command(e.args[0], argv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return nil, fmt.Errorf("executor: start %s: %w", e.args[0], err)
	}

	s := &session{
		cmd:      cmd,
		stdin:    stdin,
		logs:     make(chan string, 32),
		outcome:  make(chan result, 1),
		ctx:      ctx,
		launcher: path,
	}
	go s.readStdout(stdout, call)
	go s.drainStderr(stderr)

	if args == nil {
		args = map[string]any{}
	}
	if err := s.writeLine(map[string]any{"type": "run", "src": src, "args": args}); err != nil {
		s.finish("", fmt.Errorf("executor: send run: %w", err))
		return nil, err
	}
	return s, nil
}

func (e *Executor) materialize() (string, error) {
	f, err := os.CreateTemp("", "niq-ptc-launcher-*.mjs")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(e.launcher); err != nil {
		f.Close()
		os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

// result carries the script's terminal outcome.
type result struct {
	text string
	err  error
}

type session struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	logs     chan string
	outcome  chan result
	ctx      context.Context
	launcher string

	mu   sync.Mutex
	done bool
}

func (s *session) Logs() <-chan string { return s.logs }

func (s *session) Result(ctx context.Context) (string, error) {
	select {
	case r := <-s.outcome:
		return r.text, r.err
	case <-ctx.Done():
		s.finish("", ctx.Err())
		return "", ctx.Err()
	}
}

func (s *session) Cancel() { s.finish("", fmt.Errorf("executor: cancelled")) }

// writeLine sends one JSONL protocol message to the launcher.
func (s *session) writeLine(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return fmt.Errorf("executor: session closed")
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// readStdout consumes the launcher's protocol stream: log / call / result /
// error. It finishes the session on a terminal message or EOF.
func (s *session) readStdout(r io.Reader, call program.ToolCaller) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch str(m["type"]) {
		case "log":
			select {
			case s.logs <- str(m["text"]):
			default: // never block the sandbox on a slow log reader
			}
		case "call":
			go s.handleCall(m, call)
		case "result":
			s.finish(str(m["payload"]), nil)
			return
		case "error":
			s.finish("", fmt.Errorf("script error: %s", str(m["message"])))
			return
		}
	}
	// The stream ended without a terminal frame. Read the scanner's own error
	// first: a failed/oversized line is the actual cause, and reporting only
	// "exited without a result" would hide it.
	if err := sc.Err(); err != nil {
		s.finish("", fmt.Errorf("executor: protocol stream: %w", err))
		return
	}
	s.finish("", fmt.Errorf("executor: launcher exited without a result"))
}

// handleCall relays one SDK call to the bus via the ToolCaller and writes the
// reply back. Runs on its own goroutine so concurrent calls proceed in parallel.
func (s *session) handleCall(m map[string]any, call program.ToolCaller) {
	args, _ := m["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	value, err := call(s.ctx, str(m["target"]), event.EventType(str(m["event"])), args)
	resp := map[string]any{"type": "call_result", "id": str(m["id"])}
	if err != nil {
		resp["error"] = err.Error()
	} else {
		// The reply keeps its type on the wire: a string stays a string, a
		// structured reply arrives in the script as an object/array.
		resp["value"] = value
	}
	_ = s.writeLine(resp)
}

// drainStderr mirrors the sandbox's diagnostics (warnings, stack traces) into
// the worker log. It never fails the session — a broken stderr stream is not a
// run failure — but the error is reported so a missing tail is explainable.
func (s *session) drainStderr(r io.Reader) {
	sc := bufio.NewScanner(r)
	// Diagnostics can be one huge line (a dumped object, a minified source);
	// the default 64 KiB token limit would end the drain mid-stream.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		log.Printf("[ptc] %s", sc.Text())
	}
	if err := sc.Err(); err != nil && !isClosed(err) {
		log.Printf("[ptc] stderr stream ended: %v", err)
	}
}

// isClosed reports whether err is the pipe being closed under us (normal when
// the sandbox exits), which is not worth a log line.
func isClosed(err error) bool {
	return errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF)
}

// finish ends the session exactly once: kills the process, closes the log
// stream, and delivers the outcome.
func (s *session) finish(text string, err error) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	s.mu.Unlock()

	if s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	close(s.logs)
	s.outcome <- result{text: text, err: err}
	_ = s.cmd.Wait()
	os.Remove(s.launcher)
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
