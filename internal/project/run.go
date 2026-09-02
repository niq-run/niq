package project

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/store"
	"github.com/niq-run/niq/core/worker"
	evtsqlite "github.com/niq-run/niq/internal/project/evtstore/sqlite"
	providerpkg "github.com/niq-run/niq/internal/project/provider"
	"github.com/niq-run/niq/internal/webui"
	"github.com/niq-run/niq/pkg/eventbus"
	eventbusapi "github.com/niq-run/niq/pkg/eventbus/api"
	"github.com/niq-run/niq/pkg/eventbus/transport/httptrans"
	"github.com/niq-run/niq/pkg/eventbus/transport/inprocess"
	"github.com/niq-run/niq/pkg/services/workerhost"
	"github.com/niq-run/niq/pkg/workers/hiw"
)

// ProjectRunOptions controls running a single project instance (its own bus and
// WebUI on its own ports, its own data dirs under projects/<id>/).
type ProjectRunOptions struct {
	ProjectID string
	BusAddr   string // httptrans bus listen address (":0" = dynamic)
	WebUIAddr string // webui listen address (":0" = dynamic)
}

// RunProject loads a project's authoritative project.json and runs it as its
// own process-scoped instance: per-project registry/state, its httptrans bus
// on the project's bus port, and its WebUI on the project's webui port.
// Resolved (possibly dynamically assigned) ports are persisted back to
// project.json.
func RunProject(opts ProjectRunOptions) error {
	p, err := LoadProject(opts.ProjectID)
	if err != nil {
		return err
	}
	projDir := ProjectDir(opts.ProjectID)

	// Ports are stable: reuse the project's persisted bus/WebUI ports across runs
	// so a restarted project lands on the same addresses. Only fall back to an
	// ephemeral (:0) port when none is persisted yet (first run) or the caller
	// explicitly overrides with --bus/--webui.
	// Validate persisted ports up front: an invalid value (>65535 or <=0) would
	// fail to bind and silently fall back to a random port — warn instead.
	if p.Ports.Bus != 0 && !validPort(p.Ports.Bus) {
		log.Printf("[project %s] ignoring invalid bus port %d (must be 1-65535)", opts.ProjectID, p.Ports.Bus)
		p.Ports.Bus = 0
	}
	if p.Ports.WebUI != 0 && !validPort(p.Ports.WebUI) {
		log.Printf("[project %s] ignoring invalid webui port %d (must be 1-65535)", opts.ProjectID, p.Ports.WebUI)
		p.Ports.WebUI = 0
	}
	busAddr := opts.BusAddr
	if busAddr == "" {
		if p.Ports.Bus != 0 {
			busAddr = fmt.Sprintf("127.0.0.1:%d", p.Ports.Bus)
		} else {
			busAddr = ":0"
		}
	}
	webUIAddr := opts.WebUIAddr
	if webUIAddr == "" {
		if p.Ports.WebUI != 0 {
			webUIAddr = fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI)
		} else {
			webUIAddr = ":0"
		}
	}

	onResolved := func(bus, webui string) {
		newBus := PortOf(bus)
		if newBus == 0 {
			newBus = resolvePort(busAddr)
		}
		newWeb := PortOf(webui)
		if newWeb == 0 {
			newWeb = resolvePort(webUIAddr)
		}
		// Only persist when the ports actually changed, so a stable re-start does
		// not churn project.json.
		if newBus == p.Ports.Bus && newWeb == p.Ports.WebUI {
			return
		}
		p.Ports.Bus = newBus
		p.Ports.WebUI = newWeb
		if err := SaveProject(p); err != nil {
			log.Printf("[project %s] persist ports: %v", opts.ProjectID, err)
		}
	}

	// The persisted ports are authoritative: kill any holder so the project binds
	// its recorded address (no fallback-jumping), then record our own pid.
	freeProjectPorts(busAddr, webUIAddr)
	_ = os.WriteFile(filepath.Join(projDir, "run.pid"), []byte(strconv.Itoa(os.Getpid())), 0644)

	return runAssembly(assemblyOptions{
		IDDir:        filepath.Join(projDir, "id"),
		StateDir:     filepath.Join(projDir, "workers"),
		ProgramsRoot: filepath.Join(projDir, "programs"),
		BusAddr:      busAddr,
		WebUIAddr:    webUIAddr,
		EventsDB:     filepath.Join(projDir, "events", "events.db"),
		Banner:       "project " + opts.ProjectID,
		OnResolved:   onResolved,
		Unmanaged:    UnmanagedWorkers(p),
		ContextInfo: webui.ContextInfo{
			Mode:    "project",
			Project: opts.ProjectID,
			// Project WebUI reaches the control plane for cross-project jumps.
			ControlURL: "http://127.0.0.1:9527",
		},
	})
}

// assemblyOptions parameterizes runAssembly: where the bus/identities/workers
// live, and which network services to expose.
type assemblyOptions struct {
	IDDir        string
	StateDir     string
	ProgramsRoot string
	BusAddr      string // "" disables the HTTP bus
	WebUIAddr    string // "" disables the WebUI
	Banner       string
	OnResolved   func(bus, webui string)
	ContextInfo  webui.ContextInfo
	EventsDB     string          // SQLite event store path (empty = in-memory)
	Unmanaged    []ProjectWorker // external processes to launch after the bus is up
}

// webuiHIWID is the project-owned hiw worker that drives the WebUI. It is
// ensured (created + run + protected) only when the WebUI is started; when the
// WebUI is off, an existing webui-hiw is suspended.
const webuiHIWID = "webui-hiw"

// runAssembly is the core: build the bus, host the workers from the
// persisted store, and (optionally) expose an HTTP transport bus and a WebUI.
// Used by RunProject to bring a project instance up.
func runAssembly(opts assemblyOptions) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Identity registry (file-backed).
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(opts.IDDir, "identities.json"))
	if err != nil {
		return fmt.Errorf("project: create registry: %w", err)
	}

	// Event bus engine with an event store: SQLite (per project) when configured,
	// else in-memory.
	var evtStore store.AppendStore
	if opts.EventsDB != "" {
		if err := os.MkdirAll(filepath.Dir(opts.EventsDB), 0755); err != nil {
			return fmt.Errorf("project: mkdir events db: %w", err)
		}
		evts, err := evtsqlite.New(opts.EventsDB)
		if err != nil {
			return fmt.Errorf("project: open events db: %w", err)
		}
		evtStore = evts
	} else {
		evtStore = eventbus.NewMemoryEventStore()
	}
	engine := eventbus.NewEngine(registry, evtStore)

	// EventLog: stream every routed event to SSE subscribers.
	eventLog := eventbusapi.NewEventLog(engine, evtStore)
	engine.OnEvent(eventLog.Hook())

	// In-process listener (host-managed workers).
	listener := inprocess.NewInProcListener()
	go func() {
		for {
			ch, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
		}
	}()

	// WorkerService: control-plane lifecycle manager + persistence store.
	workerSvc := workerhost.New()
	store, err := NewFileWorkerStore(opts.StateDir)
	if err != nil {
		return fmt.Errorf("project: create worker store: %w", err)
	}
	workerSvc.SetStore(store)

	// Optional HTTP transport bus (for external/three-party workers).
	var busAddr string
	if opts.BusAddr != "" {
		startBus := func(addr string) (string, error) {
			srv := httptrans.NewServer(engine, registry, addr)
			b, err := srv.Bind()
			if err != nil {
				return "", err
			}
			go func() {
				if err := srv.Start(ctx); err != nil {
					log.Printf("[project] httptrans error: %v", err)
				}
			}()
			return b, nil
		}
		if busAddr, err = bindServer(opts.BusAddr, startBus); err != nil {
			return fmt.Errorf("project: bind bus: %w", err)
		}
	}

	// Build context and register builders.
	buildCtx := BuildContext{
		Registry:     registry,
		Listener:     listener,
		Engine:       engine,
		WorkerSvc:    workerSvc,
		EventLog:     eventLog,
		ProgramsRoot: opts.ProgramsRoot,
	}
	RegisterBuilders(buildCtx, workerSvc)

	// Gate startup on an LLM provider when any persisted worker needs one.
	if err := providerpkg.EnsureLLMConfigured(workerSvc); err != nil {
		return err
	}

	// Recover the whole worker set from the store. webui-hiw is only ensured
	// when the WebUI runs; when it does not, an existing webui-hiw is suspended.
	webUIOn := opts.WebUIAddr != ""
	var essential []worker.WorkerConfig
	var suspend []string
	if webUIOn {
		essential = []worker.WorkerConfig{{ID: webuiHIWID, Type: "hiw"}}
	} else {
		suspend = []string{webuiHIWID}
	}
	if err := workerSvc.RecoverAll(ctx, workerhost.RecoverOptions{Essential: essential, Suspend: suspend}); err != nil {
		return fmt.Errorf("project: recover workers: %w", err)
	}

	// Launch unmanaged (external) workers now that the bus is up: provision
	// each one (credential + identity) and hand it to the supervisor.
	var supervisor *UnmanagedSupervisor
	if busAddr != "" && len(opts.Unmanaged) > 0 {
		supervisor = NewUnmanagedSupervisor(LocalhostURL(busAddr), opts.StateDir, log.Printf)
		for _, spec := range opts.Unmanaged {
			s := spec
			if len(s.Command) == 0 {
				log.Printf("[project] unmanaged worker %s: empty command, skipping", s.ID)
				continue
			}
			if err := provisionUnmanaged(registry, opts.ContextInfo.Project, &s); err != nil {
				log.Printf("[project] provision unmanaged worker %s: %v", s.ID, err)
				continue
			}
			if err := supervisor.Start(s); err != nil {
				log.Printf("[project] start unmanaged worker %s: %v", s.ID, err)
			}
		}
	}

	// Optional WebUI, served on the project/control address.
	var webUIAddr string
	if opts.WebUIAddr != "" {
		if h, ok := workerSvc.Worker(webuiHIWID); ok {
			if hiwWorker, ok := h.(*hiw.Worker); ok {
				startWebUI := func(addr string) (string, error) {
					s := webui.New(hiwWorker, eventLog, engine, workerSvc, registry, addr, false)
					s.SetContext(opts.ContextInfo)
					if opts.ContextInfo.Project != "" {
						s.SetArchivedStore(projectArchiver{id: opts.ContextInfo.Project})
					}
					if supervisor != nil {
						s.SetUnmanagedController(&webuiUnmanagedAdapter{
							supervisor: supervisor,
							registry:   registry,
							projectID:  opts.ContextInfo.Project,
						})
					}
					if opts.ContextInfo.Project != "" {
						s.SetWorkerDeclRemover(webuiDeclRemover{projectID: opts.ContextInfo.Project})
					}
					b, err := s.Bind()
					if err != nil {
						return "", err
					}
					url := LocalhostURL(b)
					log.Printf("[%s] WebUI: %s", opts.Banner, url)
					fmt.Printf("%s WebUI listening at %s\n", opts.Banner, url)
					go func() {
						if err := s.Start(ctx); err != nil {
							log.Printf("[%s] webui error: %v", opts.Banner, err)
						}
					}()
					return b, nil
				}
				if webUIAddr, err = bindServer(opts.WebUIAddr, startWebUI); err != nil {
					return fmt.Errorf("project: bind webui: %w", err)
				}
			}
		}
	}
	if opts.OnResolved != nil {
		opts.OnResolved(busAddr, webUIAddr)
	}

	// Run — blocks until ctx is cancelled; ShutdownSnapshot persists state.
	fmt.Printf("%s started. Press Ctrl+C to stop.\n", opts.Banner)
	if err := workerSvc.Run(ctx); err != nil && err != context.Canceled {
		if supervisor != nil {
			supervisor.Shutdown()
		}
		return fmt.Errorf("%s: %w", opts.Banner, err)
	}
	if supervisor != nil {
		supervisor.Shutdown()
	}
	fmt.Printf("\n%s stopped.\n", opts.Banner)
	return nil
}

// validPort reports whether n is a usable TCP port (1-65535).
func validPort(n int) bool { return n > 0 && n <= 65535 }

// freeProjectPorts makes the persisted ports authoritative: if a port is currently
// occupied, it kills the holder(s) so the project binds its recorded address.
func freeProjectPorts(addrs ...string) {
	for _, a := range addrs {
		if a == "" || a == ":0" {
			continue
		}
		_, port, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		if n, err := strconv.Atoi(port); err != nil || n == 0 {
			continue
		}
		if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 200*time.Millisecond); err != nil {
			continue // free already
		} else {
			conn.Close()
		}
		log.Printf("[project] port %s occupied; killing holder(s) to reuse it", port)
		killPortHolders(port)
	}
	// Give the sockets a moment to release before binding.
	time.Sleep(300 * time.Millisecond)
}

// killPortHolders kills every process bound to a TCP port, via lsof.
func killPortHolders(port string) {
	out, err := exec.Command("lsof", "-ti", "tcp:"+port).Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		pidStr := strings.TrimSpace(line)
		if pidStr == "" {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 1 { // never kill init/pid 1
			continue
		}
		if p, err := os.FindProcess(pid); err == nil {
			log.Printf("[project] killing pid %d holding port %s", pid, port)
			_ = p.Kill()
		}
	}
}

// bindServer binds a listener at reqAddr via start; if a specific (non-":0")
// address fails to bind (e.g. a stale process still owns the port), it falls back
// to an ephemeral ":0" port so a restarted project still comes up. The resolved
// (possibly fallback) host:port is returned to the caller to be persisted.
func bindServer(reqAddr string, start func(addr string) (string, error)) (string, error) {
	resolved, err := start(reqAddr)
	if err == nil || reqAddr == ":0" {
		return resolved, err
	}
	log.Printf("[project] addr %s unavailable (%v); falling back to an ephemeral port", reqAddr, err)
	if resolved2, err2 := start(":0"); err2 == nil {
		return resolved2, nil
	}
	return "", err
}

// LocalhostURL turns a resolved listen address (e.g. "[::]:19763") into a
// clickable localhost URL. Exported for the control package, which renders
// project WebUI URLs from resolved addresses.
func LocalhostURL(resolved string) string {
	if resolved == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(resolved); err == nil {
		return "http://localhost:" + port
	}
	return "http://" + resolved
}

// PortOf parses the numeric port from a host:port address, 0 on failure.
func PortOf(addr string) int {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		if n, err := strconv.Atoi(port); err == nil {
			return n
		}
	}
	return 0
}

// resolvePort reads the numeric port of a flat ":N" or "host:N" address
// without actually binding (used only as a fallback sync for known ports).
func resolvePort(addr string) int {
	if n := PortOf(addr); n != 0 {
		return n
	}
	trimmed := strings.TrimPrefix(addr, ":")
	if n, err := strconv.Atoi(trimmed); err == nil {
		return n
	}
	return 0
}

// webuiDeclRemover implements webui.WorkerDeclRemover for a specific project,
// letting the WebUI remove a managed worker's project.json declaration on delete.
type webuiDeclRemover struct {
	projectID string
}

func (r webuiDeclRemover) RemoveDecl(id string) error {
	if err := RemoveWorkerDecl(r.projectID, id); err != nil {
		return err
	}
	return removeWorkerStateDir(r.projectID, id)
}

// webuiUnmanagedAdapter implements webui.UnmanagedController, routing the
// project WebUI's external-worker controls to the project supervisor.
type webuiUnmanagedAdapter struct {
	supervisor *UnmanagedSupervisor
	registry   corebus.IdentityRegistry
	projectID  string
}

func (a *webuiUnmanagedAdapter) Start(id string) error {
	if a.projectID == "" {
		return fmt.Errorf("unmanaged control requires a project")
	}
	p, err := LoadProject(a.projectID)
	if err != nil {
		return err
	}
	spec, ok := FindWorker(p, id)
	if !ok {
		return fmt.Errorf("worker %s not found", id)
	}
	if spec.Managed {
		return fmt.Errorf("worker %s is managed, not an external process", id)
	}
	if err := provisionUnmanaged(a.registry, a.projectID, &spec); err != nil {
		return err
	}
	return a.supervisor.Start(spec)
}

func (a *webuiUnmanagedAdapter) Stop(id string) error    { return a.supervisor.Stop(id) }
func (a *webuiUnmanagedAdapter) Restart(id string) error { return a.supervisor.Restart(id) }

// Remove stops the worker (if supervised) and deletes its project.json
// declaration so it is not relaunched next start.
func (a *webuiUnmanagedAdapter) Remove(id string) error {
	_ = a.supervisor.Stop(id)
	if a.projectID == "" {
		return fmt.Errorf("unmanaged control requires a project")
	}
	if err := RemoveWorkerDecl(a.projectID, id); err != nil {
		return err
	}
	return removeWorkerStateDir(a.projectID, id)
}

func (a *webuiUnmanagedAdapter) List() []webui.UnmanagedStatus {
	out := make([]webui.UnmanagedStatus, 0, 4)
	for _, st := range a.supervisor.List() {
		out = append(out, webui.UnmanagedStatus{ID: st.ID, Type: st.Type, State: st.State, Alive: st.Alive})
	}
	return out
}

// Declared returns every worker project.json declares as external (unmanaged),
// merging the supervisor's live state so declared-but-not-started workers show
// as stopped. These drive the UI's "start" button for idle external workers.
func (a *webuiUnmanagedAdapter) Declared() []webui.UnmanagedStatus {
	if a.projectID == "" {
		return nil
	}
	p, err := LoadProject(a.projectID)
	if err != nil {
		return nil
	}
	running := map[string]bool{}
	for _, st := range a.supervisor.List() {
		running[st.ID] = st.State == "running"
	}
	var out []webui.UnmanagedStatus
	for _, spec := range p.Workers {
		if spec.Managed {
			continue
		}
		st := webui.UnmanagedStatus{ID: spec.ID, Type: spec.Type, State: "stopped"}
		if running[spec.ID] {
			st.State = "running"
			st.Alive = true
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
