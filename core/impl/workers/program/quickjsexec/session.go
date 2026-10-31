package quickjsexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"modernc.org/quickjs"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/program"
)

// errCancelled is what a session reports after Cancel.
var errCancelled = errors.New("executor: cancelled")

// errStalled reports a script that can never finish: its async body is waiting
// on a promise nothing will resolve, and no call is in flight. Failing fast
// beats sitting on it until the run timeout.
var errStalled = errors.New("script stopped: nothing left to run and no pending call")

// result is one session's terminal outcome.
type result struct {
	value any
	err   error
}

// apply is work for the VM owner goroutine: a reply arriving from the bus is
// applied by resolving the script's promise, and resolving touches the VM, so
// it may only happen there.
type apply func(vm *quickjs.VM) error

// session is one script run.
//
// OWNERSHIP: one goroutine (the one running run) owns the VM. Everything below
// marked "owner" is touched only there — including promise settlement, which is
// why replies are posted as events instead of being applied by the relay
// goroutine that received them. QuickJS is not goroutine-safe: calling into it
// from another goroutine corrupts or crashes the process. Cancel and the
// watchdog are the only cross-goroutine contact, and both go through
// Interrupt(), the one entry point the binding declares safe from any goroutine.
type session struct {
	ctx  context.Context
	opts Options
	src  string
	args map[string]any
	call program.ToolCaller
	msg  program.MessageSender

	logs   chan string
	out    chan result
	events chan apply
	quit   chan struct{} // closed by Cancel
	stop   chan struct{} // closed when the owner goroutine exits

	finishOnce sync.Once
	quitOnce   sync.Once

	// owner-goroutine state
	pending int  // calls sent but not yet settled
	settled bool // the script's body finished (result or error)
	aborted error
	offset  int // lines of prelude, for mapping stack frames to script lines
}

func newSession(ctx context.Context, opts Options, src string, args map[string]any, call program.ToolCaller, msg program.MessageSender) *session {
	return &session{
		ctx:    ctx,
		opts:   opts,
		src:    src,
		args:   args,
		call:   call,
		msg:    msg,
		logs:   make(chan string, 32),
		out:    make(chan result, 1),
		events: make(chan apply, 64),
		quit:   make(chan struct{}),
		stop:   make(chan struct{}),
	}
}

// Logs streams the script's sdk.log / console lines; closed when the session ends.
func (s *session) Logs() <-chan string { return s.logs }

// Result blocks until the script finishes or ctx is done. On ctx expiry the
// session is cancelled and given a moment to tear down, so the caller sees the
// real cause rather than "cancelled" for what was a timeout.
func (s *session) Result(ctx context.Context) (any, error) {
	select {
	case r := <-s.out:
		return r.value, r.err
	case <-ctx.Done():
		s.Cancel()
		select {
		case r := <-s.out:
			if errors.Is(r.err, errCancelled) {
				return nil, ctx.Err()
			}
			return r.value, r.err
		case <-time.After(time.Second):
			return nil, ctx.Err()
		}
	}
}

// Cancel ends the session. It only signals: the owner goroutine closes the VM.
func (s *session) Cancel() {
	s.quitOnce.Do(func() { close(s.quit) })
}

// run owns the VM for its whole life.
func (s *session) run() {
	defer close(s.stop)

	vm, err := quickjs.NewVM()
	if err != nil {
		s.finish(nil, fmt.Errorf("executor: new vm: %w", err))
		return
	}
	value, err := s.execute(vm)
	// Close before publishing the outcome: Result's contract says the session's
	// resources are released by the time it returns.
	_ = vm.Close()
	s.finish(value, err)
}

// execute installs the SDK, runs the script, and drives its jobs to completion.
func (s *session) execute(vm *quickjs.VM) (value any, err error) {
	// A Go host function panics through the engine. Recovering here turns that
	// into a failed run instead of a dead niq.
	defer func() {
		if r := recover(); r != nil {
			value, err = nil, fmt.Errorf("executor: host call panicked: %v", r)
		}
	}()

	vm.SetMemoryLimit(s.opts.MemoryLimit)
	vm.SetMaxStackSize(s.opts.MaxStackSize)

	// Watchdog: a pure CPU loop never returns to the loop below, so the only way
	// to stop it is Interrupt from another goroutine.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-s.ctx.Done():
		case <-s.quit:
		case <-stopWatch:
			return
		}
		vm.Interrupt()
	}()

	if err := s.install(vm); err != nil {
		return nil, err
	}

	// Line offset: the prelude's lines, plus the "(async () => {" opener, so a
	// frame's line minus offset is a line of the user's own source.
	s.offset = strings.Count(prelude, "\n") + 1
	body := prelude + "(async () => {\n" + s.src + "\n})().then(__finish, __abort);\n"
	if _, err := vm.EvalValue(body, quickjs.EvalGlobal); err != nil {
		return nil, s.wrap(err)
	}

	return s.loop(vm)
}

// install wires the host functions the prelude calls and injects sdk.args.
func (s *session) install(vm *quickjs.VM) error {
	args := s.args
	if args == nil {
		args = map[string]any{}
	}
	atom, err := vm.NewAtom("__niq_args")
	if err != nil {
		return fmt.Errorf("executor: args: %w", err)
	}
	if err := vm.SetProperty(vm.GlobalObject(), atom, args); err != nil {
		return fmt.Errorf("executor: args: %w", err)
	}

	host := []struct {
		name string
		fn   quickjs.HostFunc
	}{
		{"__niq_log", s.hostLog},
		{"__niq_send", s.hostSend},
		{"__niq_finish", s.hostFinish},
		{"__niq_abort", s.hostAbort},
	}
	for _, h := range host {
		if err := vm.RegisterHostFunc(h.name, h.fn); err != nil {
			return fmt.Errorf("executor: register %s: %w", h.name, err)
		}
	}
	return nil
}

// hostLog mirrors one log line to the session's log stream. A slow reader must
// never block the VM, so a full buffer drops instead.
func (s *session) hostLog(a []any) (any, error) {
	select {
	case s.logs <- arg(a, 0):
	default:
	}
	return nil, nil
}

// hostSend starts one relay. The bus round-trip runs on its own goroutine; its
// completion is posted back as an event, because settling the promise touches
// the VM.
func (s *session) hostSend(a []any) (any, error) {
	id := arg(a, 0)
	switch arg(a, 1) {
	case "call":
		target, evt := arg(a, 2), arg(a, 3)
		var callArgs map[string]any
		if raw := arg(a, 4); raw != "" {
			_ = json.Unmarshal([]byte(raw), &callArgs)
		}
		s.pending++
		go func() {
			value, err := s.call(s.ctx, target, event.EventType(evt), callArgs)
			s.post(func(vm *quickjs.VM) error {
				s.pending--
				if err != nil {
					_, e := vm.Call("__reject", id, err.Error())
					return e
				}
				_, e := vm.Call("__settle", id, value)
				return e
			})
		}()
	case "message":
		target, text := arg(a, 2), arg(a, 3)
		if s.msg == nil {
			// Reject outside this callback: we are inside the engine here, and
			// re-entering the VM from a host function is not allowed.
			s.post(func(vm *quickjs.VM) error {
				_, e := vm.Call("__reject", id, "message is not supported by this executor")
				return e
			})
			return nil, nil
		}
		s.pending++
		go func() {
			err := s.msg(s.ctx, target, text)
			s.post(func(vm *quickjs.VM) error {
				s.pending--
				if err != nil {
					_, e := vm.Call("__reject", id, err.Error())
					return e
				}
				_, e := vm.Call("__settle", id, "sent")
				return e
			})
		}()
	}
	return nil, nil
}

// hostFinish marks the script's body as completed.
func (s *session) hostFinish([]any) (any, error) {
	s.settled = true
	return nil, nil
}

// hostAbort marks the run failed, with the error already formatted (and located)
// by the prelude.
func (s *session) hostAbort(a []any) (any, error) {
	s.settled = true
	s.aborted = errors.New(describe(arg(a, 0), s.offset, s.src))
	return nil, nil
}

// post hands one piece of work to the owner goroutine, giving up when the
// session has already ended (otherwise a late reply would leak the goroutine).
func (s *session) post(ev apply) {
	select {
	case s.events <- ev:
	case <-s.stop:
	}
}

// loop drives the VM until the script's body settles.
//
// QuickJS only runs Promise reactions and await continuations while the host
// pumps JS_ExecutePendingJob, and it knows nothing about our bus: a call that
// has been sent is invisible to the engine. So the loop alternates "run what is
// runnable" with "wait for a reply, cancellation, or the run deadline" — the Go
// equivalent of the poll loop a C host would write, with a select instead of
// poll(2) because our I/O is a channel.
func (s *session) loop(vm *quickjs.VM) (any, error) {
	for {
		n, err := vm.ExecutePendingJobs()
		if err != nil {
			return nil, s.wrap(err)
		}
		if s.settled {
			if s.aborted != nil {
				return nil, s.aborted
			}
			return s.collect(vm)
		}
		if n > 0 {
			continue // the queue may have refilled for itself
		}

		// Idle: apply at most one pending reply, then drain again.
		select {
		case ev := <-s.events:
			if err := ev(vm); err != nil {
				return nil, s.wrap(err)
			}
			continue
		default:
		}

		if s.pending == 0 {
			return nil, errStalled
		}

		select {
		case ev := <-s.events:
			if err := ev(vm); err != nil {
				return nil, s.wrap(err)
			}
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		case <-s.quit:
			return nil, errCancelled
		}
	}
}

// collect reads the run's result out of the VM as JSON text and gives it back
// its shape, so the worker's reply shaping sees the value, not an encoding.
func (s *session) collect(vm *quickjs.VM) (any, error) {
	raw, err := vm.Eval("__niq_take()", quickjs.EvalGlobal)
	if err != nil {
		return nil, s.wrap(err)
	}
	text, _ := raw.(string)
	if text == "" {
		return "", nil
	}
	var value any
	if json.Unmarshal([]byte(text), &value) != nil {
		return text, nil
	}
	return value, nil
}

// wrap turns an engine error into what the caller should see: the run's own
// cancellation/timeout first (an interrupted eval reports "InternalError:
// interrupted", which says nothing useful), then a located script error.
func (s *session) wrap(err error) error {
	if err == nil {
		return nil
	}
	select {
	case <-s.quit:
		return errCancelled
	default:
	}
	if ctxErr := s.ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var qe *quickjs.Error
	if errors.As(err, &qe) {
		return errors.New(locate(qe, s.offset, s.src))
	}
	return err
}

// finish publishes the outcome exactly once and closes the log stream.
func (s *session) finish(value any, err error) {
	s.finishOnce.Do(func() {
		close(s.logs)
		s.out <- result{value: value, err: err}
	})
}

// arg reads one host-function argument as text. A missing or non-string
// argument reads as empty rather than "<nil>".
func arg(a []any, i int) string {
	if i >= len(a) || a[i] == nil {
		return ""
	}
	if s, ok := a[i].(string); ok {
		return s
	}
	return fmt.Sprint(a[i])
}
