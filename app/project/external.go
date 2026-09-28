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
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/niq-run/niq/core/impl/eventbus"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
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
	spec    WorkerConfig
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
func (s *UnmanagedSupervisor) Start(spec WorkerConfig) error {
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

func (s *UnmanagedSupervisor) buildEnv(spec WorkerConfig) []string {
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

// buildRemoteIdentity assembles the remotable identity record for an external
// worker from its spec. Remote is set so the transport authenticates it: this
// is a worker that is allowed (indeed expected) to connect over the HTTP bus,
// unlike an in-process managed worker whose id must never be claimable there.
func buildRemoteIdentity(spec *WorkerConfig) corebus.Identity {
	subAllow := make([]event.EventPattern, 0, len(spec.Subscriptions))
	for _, s := range spec.Subscriptions {
		subAllow = append(subAllow, s.ToPattern())
	}
	if len(subAllow) == 0 {
		// A NIW far-side identity only needs to receive the collaborative
		// worker.input plus worker.discover (so it can be found / announced on
		// the far side). Defaulting it to `*` would ingest the WHOLE far-side
		// bus broadcast, and the bridge would relay (reason.start etc.) as
		// conversation — the flood seen on the far side. Other third-party
		// workers keep the `*` fallback for backward compatibility with
		// broadcast-driven peers.
		if spec.Type == "remote-niw" {
			subAllow = []event.EventPattern{
				{Type: event.TypeWorkerInput},
				{Type: "worker.discover"},
			}
		} else {
			subAllow = []event.EventPattern{{Type: "*"}}
		}
	}
	pubAllow := make([]event.PublishPattern, 0, len(spec.Publish))
	for _, s := range spec.Publish {
		pubAllow = append(pubAllow, s.ToPublishPattern())
	}
	if len(pubAllow) == 0 {
		// A NIW far-side identity only publishes what the bridge needs on that
		// bus: forward collaborative worker.input, reply to control (request.*),
		// and announce presence. Defaulting it to `*` would let the dialed-in
		// NIW publish arbitrary event types / reach any worker on the far side.
		// Other third-party workers keep the `*` fallback.
		if spec.Type == "remote-niw" {
			pubAllow = []event.PublishPattern{
				event.NewPublishPattern("worker.input"),
				event.NewPublishPattern("request.*"),
				event.NewPublishPattern("worker.ready"),
			}
		} else {
			pubAllow = []event.PublishPattern{event.NewPublishPattern("*")}
		}
	}
	return corebus.Identity{
		WorkerID:       spec.ID,
		Type:           spec.Type,
		PublishAllow:   pubAllow,
		SubscribeAllow: subAllow,
		Credential:     spec.Credential,
		Remote:         true,
	}
}

// persistRemoteTokenTTL is the DEFAULT lifetime of a provisioned remote
// identity's signed token (180 days). A provisioned remote worker reuses the
// persisted token across bus restarts, so the cap forces periodic
// re-provisioning rather than infinitely-valid credentials. The expiry can be
// overridden per-worker via WorkerConfig.TokenTTLSeconds.
const persistRemoteTokenTTL = 180 * 24 * time.Hour

// tempTokenTTL is the DEFAULT lifetime of a launched third-party worker's token
// (30 days). Lives in the in-memory tier of the identity registry (dies with
// the main process), so 30 days is a wall-clock backstop against a leaked
// token, not the operative lifetime; the host re-mints a fresh token on every
// (re)spawn. Overridable per-worker via WorkerConfig.TokenTTLSeconds.
const tempTokenTTL = 30 * 24 * time.Hour

// ttlFor resolves a worker's token lifetime: an explicit TokenTTLSeconds wins,
// otherwise the default for the provisioning class.
func ttlFor(spec *WorkerConfig, def time.Duration) time.Duration {
	if spec != nil && spec.TokenTTLSeconds != nil && *spec.TokenTTLSeconds > 0 {
		return time.Duration(*spec.TokenTTLSeconds) * time.Second
	}
	return def
}

// provisionUnmanaged provisions a remotely-connected worker — one the host did
// NOT launch (a third party dials in on its own) — with a durable identity and a
// long-lived signed token. First launch mints the token and persists it to
// project.json; later runs reuse it (the bus verifies the token by signature,
// and the signing secret also persists). The identity (Remote=true) is
// registered durably so it survives restarts.
func provisionUnmanaged(registry corebus.IdentityRegistry, projectID string, spec *WorkerConfig, signer *eventbus.TokenSigner) error {
	if signer == nil {
		return fmt.Errorf("provision %s: no token signer configured", spec.ID)
	}
	if spec.Credential == "" {
		tok, err := signer.Mint(spec.ID, ttlFor(spec, persistRemoteTokenTTL))
		if err != nil {
			return err
		}
		spec.Credential = tok
		if projectID != "" {
			if err := persistWorkerCredential(projectID, spec.ID, spec.Credential); err != nil {
				return err
			}
		}
	}
	return registerIdentity(registry, buildRemoteIdentity(spec))
}

// provisionTemp provisions a third-party worker the HOST itself launched (MCP
// stdio server, custom agent): a short-lived signed token and an identity that
// lives in the in-memory tier of a LayeredRegistry for the duration of the main
// process. Nothing is persisted to project.json. The token is handed to the
// spawned child via its environment (NIQ_WORKER_CREDENTIAL) and dies when the
// process (or the identity's memory tier) goes away.
func provisionTemp(registry corebus.IdentityRegistry, signer *eventbus.TokenSigner, spec *WorkerConfig) error {
	if signer == nil {
		return fmt.Errorf("provision temp %s: no token signer configured", spec.ID)
	}
	tok, err := signer.Mint(spec.ID, ttlFor(spec, tempTokenTTL))
	if err != nil {
		return err
	}
	spec.Credential = tok
	return registerTempIdentity(registry, buildRemoteIdentity(spec))
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
