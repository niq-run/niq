// Supervision for third-party OS processes the host launches: launch,
// crash-restart with backoff, manual stop/restart, reap on shutdown. It knows
// nothing about manifests, placements or credentials — the caller provisions
// first and hands a ready Spec to Start.
package process

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Environment variables passed to managed-process workers so they can connect
// to the bus on their own.
const (
	EnvBusURL     = "NIQ_BUS_URL"
	EnvWorkerID   = "NIQ_WORKER_ID"
	EnvWorkerCred = "NIQ_WORKER_CREDENTIAL"
	EnvStateDir   = "NIQ_STATE_DIR"
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
	// stableWindow is the uptime after which the crash backoff resets.
	stableWindow = 30 * time.Second
)

// Supervisor launches and supervises OS child processes: it starts them,
// restarts them with exponential backoff on unexpected exit, stops them on
// request, and reaps them on shutdown. It does not own credential provisioning
// or bus registration — the control plane provisions a worker first and hands a
// ready Spec to Start.
type Supervisor struct {
	busURL       string
	workersRoot  string // per-worker stdout logs live under <root>/<id>/stdout.log
	logf         func(format string, args ...any)
	initialDelay time.Duration
	maxDelay     time.Duration
	stableAfter  time.Duration

	mu    sync.Mutex
	procs map[string]*procState
	wg    sync.WaitGroup
}

type procState struct {
	spec    Spec
	ctx     context.Context
	cancel  context.CancelFunc
	alive   bool
	stopped bool
	// cmd is the currently-running child (nil when idle / between restarts).
	// Guarded by mu together with alive.
	cmd *exec.Cmd
	mu  sync.Mutex
}

// NewSupervisor creates a supervisor that launches worker processes
// against the given bus URL. workersRoot is the project's workers/ directory;
// each worker's stdout is logged to <workersRoot>/<id>/stdout.log (empty to
// fall back to the supervisor's own stdout/stderr).
func NewSupervisor(busURL, workersRoot string, logf func(string, ...any)) *Supervisor {
	if logf == nil {
		logf = log.Printf
	}
	return &Supervisor{
		busURL:       busURL,
		workersRoot:  workersRoot,
		logf:         logf,
		initialDelay: initialBackoff,
		maxDelay:     maxBackoff,
		stableAfter:  stableWindow,
		procs:        map[string]*procState{},
	}
}

// Start launches a worker process from a ready declaration (credential already
// provisioned). To guarantee at most one live child per worker id, it first
// kills any existing (possibly stale) child for that id, then launches fresh.
// The process is supervised: an unexpected exit restarts it with exponential
// backoff.
func (s *Supervisor) Start(spec Spec) error {
	if len(spec.Command) == 0 {
		return fmt.Errorf("worker %s: command is required", spec.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// If a worker with this id already has a live child (e.g. a restart raced
	// with a still-running instance), kill it first so the bus never sees two
	// connections for the same worker id. The old supervise goroutine keeps its
	// own captured context and will reap the child, then exit.
	if old, ok := s.procs[spec.ID]; ok {
		if !old.isStopped() {
			s.logf("[process] %s: replacing running instance before start", spec.ID)
			old.cancel()
			s.killCurrent(old)
		}
	}

	// Use a fresh procState each launch so the new supervise goroutine never
	// shares (or races on) a context with a previous one.
	ctx, cancel := context.WithCancel(context.Background())
	st := &procState{spec: spec, ctx: ctx, cancel: cancel}
	s.procs[spec.ID] = st
	s.wg.Add(1)
	go s.supervise(st)
	return nil
}

// killCurrent terminates the child currently recorded on st, if any. Safe to
// call with st.mu held or not; it takes the lock internally. The child is left
// for the owning supervise goroutine to reap (it is already blocked on Wait and
// will return once the process dies). Callers should cancel st.ctx first.
func (s *Supervisor) killCurrent(st *procState) {
	st.mu.Lock()
	cmd := st.cmd
	st.cmd = nil
	st.alive = false
	st.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		killTree(cmd) // kill the whole process group
	}
}

// Stop stops a supervised worker: cancels its supervision context (which kills
// the child process) and marks it stopped so it is not restarted. A later Start
// or Restart relaunches it.
func (s *Supervisor) Stop(id string) error {
	s.mu.Lock()
	st, ok := s.procs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("worker %s is not supervised", id)
	}
	st.mu.Lock()
	st.stopped = true
	st.mu.Unlock()
	st.cancel()
	return nil
}

// Remove stops a supervised worker and waits for its supervision loop to exit,
// so the process is gone (and reaped) before the caller deletes anything else.
// A worker that was never supervised is a no-op.
func (s *Supervisor) Remove(id string) error {
	s.mu.Lock()
	st, ok := s.procs[id]
	if ok && !st.isStopped() {
		st.mu.Lock()
		st.stopped = true
		st.mu.Unlock()
		st.cancel()
	}
	s.mu.Unlock()
	if !ok {
		return nil
	}
	// Wait for the loop to notice the cancellation and reap the child. The
	// global wg covers every worker, so poll the entry instead: once the
	// supervise goroutine returns it leaves the map in shutdown order, and the
	// child's Wait has already completed.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		alive := st.alive
		st.mu.Unlock()
		if !alive {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	delete(s.procs, id)
	s.mu.Unlock()
	return nil
}

// Destroy stops a supervised worker and removes its per-worker directory
// (stdout log and any third-party state). Restart uses Remove, so logs survive
// a restart; only an explicit destroy deletes them.
func (s *Supervisor) Destroy(id string) error {
	if err := s.Remove(id); err != nil {
		return err
	}
	if s.workersRoot == "" {
		return nil
	}
	return os.RemoveAll(filepath.Join(s.workersRoot, sanitizeID(id)))
}

// Restart stops a supervised worker (if running) and starts it again. The
// stored declaration is reused — credential/registration are untouched.
func (s *Supervisor) Restart(id string) error {
	s.mu.Lock()
	st, ok := s.procs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("worker %s is not supervised", id)
	}
	st.mu.Lock()
	spec := st.spec
	st.mu.Unlock()
	if err := s.Stop(id); err != nil {
		return err
	}
	return s.Start(spec)
}

// ProcessStatus is a read-only view of a supervised worker process.
type ProcessStatus struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	State string `json:"state"` // "running" | "stopped"
	Alive bool   `json:"alive"`
}

// Get returns one supervised worker's status.
func (s *Supervisor) Get(id string) (ProcessStatus, bool) {
	s.mu.Lock()
	st, ok := s.procs[id]
	s.mu.Unlock()
	if !ok {
		return ProcessStatus{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	state := "running"
	if st.stopped {
		state = "stopped"
	}
	return ProcessStatus{ID: st.spec.ID, Type: st.spec.Type, State: state, Alive: st.alive}, true
}

// List returns the supervised workers, sorted by id.
func (s *Supervisor) List() []ProcessStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProcessStatus, 0, len(s.procs))
	for _, st := range s.procs {
		st.mu.Lock()
		state := "running"
		if st.stopped {
			state = "stopped"
		}
		out = append(out, ProcessStatus{
			ID: st.spec.ID, Type: st.spec.Type, State: state, Alive: st.alive,
		})
		st.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Shutdown stops every supervised worker and waits for the supervision loops
// to exit. Called on project shutdown.
func (s *Supervisor) Shutdown() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.procs))
	for id := range s.procs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		_ = s.Stop(id)
	}
	s.wg.Wait()
}

func (s *Supervisor) supervise(st *procState) {
	defer s.wg.Done()
	delay := s.initialDelay
	for {
		select {
		case <-st.ctx.Done():
			return
		default:
		}
		cmd := exec.Command(st.spec.Command[0], st.spec.Command[1:]...)
		cmd.Env = s.buildEnv(st.spec)
		if st.spec.Cwd != "" {
			cmd.Dir = st.spec.Cwd
		}

		// Log the worker's stdout/stderr to <workersRoot>/<id>/stdout.log so it
		// can be inspected after the fact instead of being lost on the
		// supervisor's own stdout.
		if w := s.logOutput(st.spec.ID); w != nil {
			cmd.Stdout = w
			cmd.Stderr = w
		} else {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
		// Run the worker in its own process group so cancelling the context can
		// kill the whole tree (platform shim; see process_unix.go / process_windows.go).
		setOwnProcessGroup(cmd)

		start := time.Now()
		if err := cmd.Start(); err != nil {
			s.logf("[process] %s launch failed: %v", st.spec.ID, err)
			delay = nextBackoff(delay, s.maxDelay)
		} else {
			st.mu.Lock()
			st.alive = true
			st.cmd = cmd
			st.mu.Unlock()
			killDone := make(chan struct{})
			go func() {
				select {
				case <-st.ctx.Done():
					killTree(cmd)
				case <-killDone:
				}
			}()
			err := cmd.Wait()
			close(killDone)
			st.mu.Lock()
			st.alive = false
			st.cmd = nil
			st.mu.Unlock()
			if time.Since(start) > s.stableAfter {
				delay = s.initialDelay
			} else {
				delay = nextBackoff(delay, s.maxDelay)
			}
			s.logf("[process] %s exited: %v; restart in %s", st.spec.ID, err, delay)
		}

		select {
		case <-st.ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// logOutput opens (creating if needed) an append-only log file for a worker's
// stdout/stderr at <workersRoot>/<id>/stdout.log. Returns nil when no root is
// configured, so callers fall back to the supervisor's own stdout/stderr.
func (s *Supervisor) logOutput(id string) *os.File {
	if s.workersRoot == "" {
		return nil
	}
	dir := filepath.Join(s.workersRoot, sanitizeID(id))
	if err := os.MkdirAll(dir, 0755); err != nil {
		s.logf("[process] %s: cannot create log dir %s: %v", id, dir, err)
		return nil
	}
	w, err := os.OpenFile(filepath.Join(dir, "stdout.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		s.logf("[process] %s: cannot open stdout.log: %v", id, err)
		return nil
	}
	return w
}

func (s *Supervisor) buildEnv(spec Spec) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		EnvBusURL+"="+s.busURL,
		EnvWorkerID+"="+spec.ID,
		EnvWorkerCred+"="+spec.Credential,
	)
	// Each external worker gets its own persistent state directory under the
	// project's workers/ dir. It persists across restarts; what the worker keeps
	// in it is entirely its own business (the host does not interpret it). The
	// dir is created so the worker can rely on it existing and being writable.
	if s.workersRoot != "" {
		stateDir := filepath.Join(s.workersRoot, sanitizeID(spec.ID))
		env = append(env, EnvStateDir+"="+stateDir)
		if err := os.MkdirAll(stateDir, 0755); err != nil {
			s.logf("[process] %s: cannot create state dir %s: %v", spec.ID, stateDir, err)
		}
	}
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (st *procState) isStopped() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.stopped
}

func nextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		return max
	}
	return d
}
