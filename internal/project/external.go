// Unmanaged worker support: external processes that the project launches after
// the bus is up, passing the bus endpoint + identity via environment variables
// so they can connect on their own (e.g. MCP-style stdio servers, custom agents).
//
// Responsibility split:
//   - provisionUnmanaged: credential provisioning + bus identity registration.
//   - UnmanagedSupervisor: pure process supervision — launch, crash-restart
//     with backoff, manual stop/restart, reap on shutdown.
package project

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// Environment variables passed to unmanaged workers so they can connect to the
// bus on their own.
const (
	envBusURL     = "NIQ_BUS_URL"
	envWorkerID   = "NIQ_WORKER_ID"
	envWorkerCred = "NIQ_WORKER_CREDENTIAL"
	envStateDir   = "NIQ_STATE_DIR"
)

const (
	initialBackoff = time.Second
	maxBackoff     = 30 * time.Second
	// stableWindow is the uptime after which the crash backoff resets.
	stableWindow = 30 * time.Second
)

// UnmanagedSupervisor launches and supervises external worker processes: it
// starts them, restarts them with exponential backoff on unexpected exit,
// stops them on request, and reaps them on shutdown. It does not own
// credential provisioning or bus registration — callers provision a worker
// (provisionUnmanaged) and hand a ready spec to Start.
type UnmanagedSupervisor struct {
	busURL       string
	workersRoot  string // project workers/ dir; per-worker stdout logs live under <root>/<id>/stdout.log
	logf         func(format string, args ...any)
	initialDelay time.Duration
	maxDelay     time.Duration
	stableAfter  time.Duration

	mu    sync.Mutex
	procs map[string]*procState
	wg    sync.WaitGroup
}

type procState struct {
	spec    ProjectWorker
	ctx     context.Context
	cancel  context.CancelFunc
	alive   bool
	stopped bool
	// cmd is the currently-running child (nil when idle / between restarts).
	// Guarded by mu together with alive.
	cmd *exec.Cmd
	mu  sync.Mutex
}

// NewUnmanagedSupervisor creates a supervisor that launches external workers
// against the given bus URL. workersRoot is the project's workers/ directory;
// each worker's stdout is logged to <workersRoot>/<id>/stdout.log (empty to
// fall back to the supervisor's own stdout/stderr).
func NewUnmanagedSupervisor(busURL, workersRoot string, logf func(string, ...any)) *UnmanagedSupervisor {
	if logf == nil {
		logf = log.Printf
	}
	return &UnmanagedSupervisor{
		busURL:       busURL,
		workersRoot:  workersRoot,
		logf:         logf,
		initialDelay: initialBackoff,
		maxDelay:     maxBackoff,
		stableAfter:  stableWindow,
		procs:        map[string]*procState{},
	}
}

// Start launches an external worker from a ready spec (credential already
// provisioned). To guarantee at most one live child per worker id, it first
// kills any existing (possibly stale) child for that id, then launches fresh.
// The process is supervised: an unexpected exit restarts it with exponential
// backoff.
func (s *UnmanagedSupervisor) Start(spec ProjectWorker) error {
	if len(spec.Command) == 0 {
		return fmt.Errorf("unmanaged worker %s: command is required", spec.ID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// If a worker with this id already has a live child (e.g. a restart raced
	// with a still-running instance), kill it first so the bus never sees two
	// connections for the same worker id. The old supervise goroutine keeps its
	// own captured context and will reap the child, then exit.
	if old, ok := s.procs[spec.ID]; ok {
		if !old.isStopped() {
			s.logf("[unmanaged] %s: replacing running instance before start", spec.ID)
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
func (s *UnmanagedSupervisor) killCurrent(st *procState) {
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
// the child process via exec.CommandContext) and marks it stopped so it is not
// restarted. A later Start or Restart relaunches it.
func (s *UnmanagedSupervisor) Stop(id string) error {
	s.mu.Lock()
	st, ok := s.procs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unmanaged worker %s not found", id)
	}
	st.mu.Lock()
	st.stopped = true
	st.mu.Unlock()
	st.cancel()
	return nil
}

// Restart stops a supervised worker (if running) and starts it again. The
// stored spec is reused — credential/registration are untouched.
func (s *UnmanagedSupervisor) Restart(id string) error {
	s.mu.Lock()
	st, ok := s.procs[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("unmanaged worker %s not found", id)
	}
	st.mu.Lock()
	spec := st.spec
	st.mu.Unlock()
	if err := s.Stop(id); err != nil {
		return err
	}
	return s.Start(spec)
}

// UnmanagedStatus is a read-only view of a supervised external worker.
type UnmanagedStatus struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	State string `json:"state"` // "running" | "stopped"
	Alive bool   `json:"alive"`
}

// List returns the supervised external workers, sorted by id.
func (s *UnmanagedSupervisor) List() []UnmanagedStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UnmanagedStatus, 0, len(s.procs))
	for _, st := range s.procs {
		st.mu.Lock()
		state := "running"
		if st.stopped {
			state = "stopped"
		}
		out = append(out, UnmanagedStatus{
			ID: st.spec.ID, Type: st.spec.Type, State: state, Alive: st.alive,
		})
		st.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Shutdown stops every supervised worker and waits for the supervision loops
// to exit. Called on project shutdown.
func (s *UnmanagedSupervisor) Shutdown() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.procs))
	for id := range s.procs {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.Stop(id)
	}
	s.wg.Wait()
}

func (s *UnmanagedSupervisor) supervise(st *procState) {
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
		// kill the whole tree (platform shim; see proc_unix.go / proc_windows.go).
		setOwnProcessGroup(cmd)

		start := time.Now()
		if err := cmd.Start(); err != nil {
			s.logf("[unmanaged] %s launch failed: %v", st.spec.ID, err)
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
			s.logf("[unmanaged] %s exited: %v; restart in %s", st.spec.ID, err, delay)
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
func (s *UnmanagedSupervisor) logOutput(id string) *os.File {
	if s.workersRoot == "" {
		return nil
	}
	dir := filepath.Join(s.workersRoot, sanitizeID(id))
	if err := os.MkdirAll(dir, 0755); err != nil {
		s.logf("[unmanaged] %s: cannot create log dir %s: %v", id, dir, err)
		return nil
	}
	w, err := os.OpenFile(filepath.Join(dir, "stdout.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		s.logf("[unmanaged] %s: cannot open stdout.log: %v", id, err)
		return nil
	}
	return w
}

func (s *UnmanagedSupervisor) buildEnv(spec ProjectWorker) []string {
	env := os.Environ()
	env = append(env,
		envBusURL+"="+s.busURL,
		envWorkerID+"="+spec.ID,
		envWorkerCred+"="+spec.Credential,
	)
	// Each external worker gets its own persistent state directory under the
	// project's workers/ dir. It persists across restarts; what the worker keeps
	// in it is entirely its own business (the host does not interpret it). The
	// dir is created so the worker can rely on it existing and being writable.
	if s.workersRoot != "" {
		stateDir := filepath.Join(s.workersRoot, sanitizeID(spec.ID))
		env = append(env, envStateDir+"="+stateDir)
		if err := os.MkdirAll(stateDir, 0755); err != nil {
			s.logf("[unmanaged] %s: cannot create state dir %s: %v", spec.ID, stateDir, err)
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

// provisionUnmanaged ensures an unmanaged worker can connect to the bus: it
// generates and persists a credential on first launch (reusing it afterwards —
// the bus only recognizes credentials) and registers the worker's identity with
// the registry. It mutates spec.Credential when generating a fresh one.
func provisionUnmanaged(registry corebus.IdentityRegistry, projectID string, spec *ProjectWorker) error {
	if spec.Credential == "" {
		cred, err := randomCredential()
		if err != nil {
			return err
		}
		spec.Credential = cred
		if projectID != "" {
			if err := persistWorkerCredential(projectID, spec.ID, cred); err != nil {
				return err
			}
		}
	}

	subAllow := make([]event.EventPattern, 0, len(spec.Subscriptions))
	for _, s := range spec.Subscriptions {
		subAllow = append(subAllow, s.ToPattern())
	}
	if len(subAllow) == 0 {
		subAllow = []event.EventPattern{{Type: "*"}}
	}
	pubAllow := make([]event.PublishPattern, 0, len(spec.Publish))
	for _, s := range spec.Publish {
		pubAllow = append(pubAllow, s.ToPublishPattern())
	}
	if len(pubAllow) == 0 {
		pubAllow = []event.PublishPattern{event.NewPublishPattern("*")}
	}
	identity := corebus.Identity{
		WorkerID:       spec.ID,
		Type:           spec.Type,
		PublishAllow:   pubAllow,
		SubscribeAllow: subAllow,
		Credential:     spec.Credential,
	}
	if err := registry.Register(identity); err != nil {
		if !strings.Contains(err.Error(), "already registered") {
			return err
		}
		// Re-registration after a restart: the identity persists in the
		// registry file. Refresh the allow lists, and re-register the whole
		// identity if the credential diverged.
		existing, ok := registry.Lookup(spec.ID)
		if !ok {
			return err
		}
		if existing.Credential != spec.Credential {
			if err := registry.Revoke(spec.ID); err != nil {
				return err
			}
			return registry.Register(identity)
		}
		return registry.Update(spec.ID, pubAllow, subAllow)
	}
	return nil
}

// persistWorkerCredential writes a generated credential back to the worker's
// project.json entry so it is reused across restarts.
func persistWorkerCredential(projectID, workerID, cred string) error {
	p, err := LoadProject(projectID)
	if err != nil {
		return err
	}
	for i := range p.Workers {
		if p.Workers[i].ID == workerID {
			p.Workers[i].Credential = cred
			return SaveProject(p)
		}
	}
	return fmt.Errorf("worker %s not found in project %s", workerID, projectID)
}

func randomCredential() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
