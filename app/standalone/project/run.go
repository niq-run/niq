package project

import (
	"context"
	"crypto/rand"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/niq-run/niq/app/standalone/home"
	"github.com/niq-run/niq/app/standalone/store/migrate"
	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	"github.com/niq-run/niq/app/standalone/store"
	"github.com/niq-run/niq/app/standalone/store/event"
	"github.com/niq-run/niq/app/standalone/store/identity"
	projworker "github.com/niq-run/niq/app/standalone/worker"
	projprocess "github.com/niq-run/niq/app/standalone/worker/process"
	"github.com/niq-run/niq/app/standalone/shell/webui"
	"github.com/niq-run/niq/core/impl/busctl"
	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/eventbus/transport/httptrans"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/iface"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/worker"
)

// ProjectRunOptions controls running a single project instance (its own bus and
// WebUI on its own ports, its own data dirs under projects/<id>/).
type ProjectRunOptions struct {
	ProjectID string
	BusAddr   string // httptrans bus listen address (":0" = dynamic)
	WebUIAddr string // webui listen address (":0" = dynamic)
	WebUIAuth string // optional user:pass basic-auth spec for the WebUI (empty = disabled)
}

// RunProject loads a project's authoritative project.json and runs it as its
// own process-scoped instance: per-project registry/state, its httptrans bus
// on the project's bus port, and its WebUI on the project's webui port.
// Resolved (possibly dynamically assigned) ports are persisted back to
// project.json.
func RunProject(opts ProjectRunOptions) error {
	// One-time conversion of legacy project files; the runtime below reads
	// schema 2 only.
	if err := migrate.EnsureProject(ProjectDir(opts.ProjectID)); err != nil {
		return err
	}
	p, err := LoadProject(opts.ProjectID)
	if err != nil {
		return err
	}
	projDir := ProjectDir(opts.ProjectID)
	workerSet, err := store.LoadWorkers(projDir)
	if err != nil {
		return err
	}

	// Ports are stable: reuse the project's persisted bus/WebUI ports across runs
	// so a restarted project lands on the same addresses. Only fall back to an
	// ephemeral port when none is persisted yet (first run) or the caller
	// explicitly overrides with --bus/--webui. The WebUI falls back to a loopback
	// ephemeral port: first run must not expose it on every interface.
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
			// Bind the event bus on all interfaces so remote workers can connect
			// directly (authenticated by their bus credential), not through the
			// control plane. The WebUI — below — stays loopback-only: it is the
			// thing meant to be reached through the control plane.
			busAddr = fmt.Sprintf(":%d", p.Ports.Bus)
		} else {
			busAddr = ":0"
		}
	}
	webUIAddr := opts.WebUIAddr
	if webUIAddr == "" {
		if p.Ports.WebUI != 0 {
			webUIAddr = fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI)
		} else {
			webUIAddr = "127.0.0.1:0"
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
		StateDir:     filepath.Join(projDir, "workers"),
		ProgramsRoot: filepath.Join(projDir, "programs"),
		ProjDir:      projDir,
		UploadDir:    p.UploadDir,
		Workers:      workerSet.Workers,
		BusAddr:      busAddr,
		WebUIAddr:    webUIAddr,
		WebUIAuth:    opts.WebUIAuth,
		EventsDB:     filepath.Join(projDir, "events", "events.db"),
		Banner:       "project " + opts.ProjectID,
		OnResolved:   onResolved,
		ContextInfo: webui.ContextInfo{
			Mode:    "project",
			Project: opts.ProjectID,
			BusPort: PortOf(busAddr),
		},
	})
}

// assemblyOptions parameterizes runAssembly: where the bus/identities/workers
// live, and which network services to expose.
type assemblyOptions struct {
	StateDir     string
	ProgramsRoot string
	ProjDir      string         // ~/.niq/projects/<id>; anchors the webui upload dir
	UploadDir    string         // optional upload_dir override from the project config
	Workers      []WorkerConfig // the declared worker set: recovery + spawn input
	BusAddr      string         // "" disables the HTTP bus
	WebUIAddr    string         // "" disables the WebUI
	WebUIAuth    string         // optional user:pass basic-auth spec for the WebUI ("" = disabled)
	Banner       string
	OnResolved   func(bus, webui string)
	ContextInfo  webui.ContextInfo
	EventsDB     string // SQLite event store path (empty = in-memory)
}

// hostWorkerID is the lifecycle control plane. It is protected: nothing may
// suspend, restart or destroy it.
const hostWorkerID = projworker.DefaultHostWorkerID

// runAssembly is the core: build the bus, host the workers from the
// persisted store, and (optionally) expose an HTTP transport bus and a WebUI.
// Used by RunProject to bring a project instance up.
func runAssembly(opts assemblyOptions) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Resolve WebUI auth up front: enabling it when credentials are available,
	// and warning when a remote-reachable bind has no auth configured. Auth is
	// opt-in: a loopback bind needs none, and an exposed bind without credentials
	// is the operator's explicit choice.
	var webUIAuthUser, webUIAuthPass string
	var webUIAuthEnabled bool
	if opts.WebUIAddr != "" {
		user, pass, enabled, warning := webui.StartupAuth(opts.WebUIAddr, opts.WebUIAuth, home.AuthConfigPath())
		webUIAuthUser, webUIAuthPass, webUIAuthEnabled = user, pass, enabled
		if warning != "" {
			fmt.Fprintln(os.Stderr, warning)
		}
	}

	// Identity registry: two tiers behind one IdentityRegistry. The durable tier
	// is projected from the bus identity store at startup; the temporary tier
	// holds identities of the third-party processes the host itself launches,
	// which live only as long as this process.
	registry := eventbus.NewLayeredRegistry(eventbus.NewMemoryIdentityRegistry())

	// Token signer: the bus's single issuing/verifying authority for remote
	// connections. Rooted on a persisted per-project secret so long-lived
	// provisioned tokens survive a restart.
	secret, err := loadOrCreateRootSecret(opts.ProjDir)
	if err != nil {
		return fmt.Errorf("project: create signer: %w", err)
	}
	signer := eventbus.NewTokenSigner(secret)

	// Bus identity control plane: the single durable owner of who may say what.
	// It is seeded BEFORE the bus starts serving, so a remote worker can
	// authenticate on its first reconnect.
	identityStore, err := identity.New(filepath.Join(opts.ProjDir, "identities.json"))
	if err != nil {
		return fmt.Errorf("project: open identity store: %w", err)
	}
	busControl := busctl.New(identityStore, registry)
	// Archived workers keep their durable grants but are not part of the running
	// swarm, so they are not projected at startup.
	manifests := store.NewManifest(opts.ProjDir)
	archivedManifests, err := manifests.List(true)
	if err != nil {
		return fmt.Errorf("project: list archived manifests: %w", err)
	}
	archivedIDs := make([]string, 0, len(archivedManifests))
	for _, m := range archivedManifests {
		archivedIDs = append(archivedIDs, m.ID)
	}
	if err := busControl.Seed(archivedIDs); err != nil {
		return fmt.Errorf("project: seed identities: %w", err)
	}

	// Event bus engine with an event store: SQLite (per project) when configured,
	// else in-memory.
	var evtStore event.AppendStore
	if opts.EventsDB != "" {
		if err := os.MkdirAll(filepath.Dir(opts.EventsDB), 0755); err != nil {
			return fmt.Errorf("project: mkdir events db: %w", err)
		}
		evts, err := eventstore.New(opts.EventsDB)
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

	// Runtime state layout for in-process workers.
	stateStore, err := store.NewState(opts.StateDir)
	if err != nil {
		return fmt.Errorf("project: create worker store: %w", err)
	}

	// Optional HTTP transport bus (for external/three-party workers).
	var busAddr string
	if opts.BusAddr != "" {
		startBus := func(addr string) (string, error) {
			srv := httptrans.NewServer(engine, registry, signer, addr)
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

	// The process supervisor exists for the whole bus lifetime: the WebUI and
	// agents start process workers through it at runtime, not just at boot.
	var supervisor *projprocess.Supervisor
	if busAddr != "" {
		supervisor = projprocess.NewSupervisor(LocalhostURL(busAddr), opts.StateDir, log.Printf)
	}

	// The worker control plane: ONE owner of every placement and of the
	// manifest. The host worker drives it over the bus; this assembly only
	// injects the providers and starts/stops it.
	protected := []string{hostWorkerID}
	if opts.WebUIAddr != "" {
		protected = append(protected, projworker.DefaultWebUIID)
	}
	cp := projworker.New(projworker.Config{
		ProjectID:  opts.ContextInfo.Project,
		Manifests:  manifests,
		StateStore: stateStore,
		Registry:   registry,
		Bus:        busControl,
		Signer:     signer,
		Engine:     engine,
		Supervisor: supervisor,
		Protect:    protected,
	})

	buildCtx := projworker.BuildContext{
		Registry:      registry,
		Bus:           busControl,
		Listener:      listener,
		Engine:        engine,
		Manager:       cp.Manager(),
		WorkerBackend: cp.HostBackend(),
		EventLog:      eventLog,
		EventStore:    evtStore,
		ProgramsRoot:  opts.ProgramsRoot,
		StateDir:      opts.StateDir,
	}
	cp.RegisterBuilders(buildCtx)

	// Gate startup on an LLM provider when any declared in-process worker needs
	// one. This is a preflight check, not a control-plane concern, so it reads
	// the manifest store directly.
	declared, err := manifests.List(false)
	if err != nil {
		return fmt.Errorf("project: list manifests: %w", err)
	}
	inProc := make([]worker.WorkerConfig, 0, len(declared))
	for _, m := range declared {
		if projworker.IsInProcess(*m) {
			inProc = append(inProc, projworker.SpawnConfig(*m))
		}
	}
	if err := providerpkg.EnsureLLMConfigured(inProc); err != nil {
		return err
	}

	// Recover the whole declared worker set. The control plane lists the
	// manifests itself: the host is protected (it is the lifecycle control
	// plane), and so is the WebUI's worker, joined directly below.
	if err := cp.Recover(ctx); err != nil {
		return fmt.Errorf("project: recover workers: %w", err)
	}

	// Optional WebUI: the app IS the backend of its interface worker. It is built
	// first (it holds no bus handle yet), injected into the worker the assembly
	// joins here, and only then binds HTTP — the handle it sends with arrives
	// through SetHandler before the worker starts.
	var webUIAddr string
	if opts.WebUIAddr != "" {
		app := webui.New(eventLog, engine, cp.Manifest(), registry, opts.WebUIAddr, false)
		app.SetContext(opts.ContextInfo)
		app.SetProjectDir(opts.ProjDir)
		app.SetBusControl(busControl)
		if webUIAuthEnabled {
			app.SetBasicAuth(webUIAuthUser, webUIAuthPass)
			log.Printf("[project] WebUI remote access protected with basic auth (localhost stays open)")
		}
		if opts.UploadDir != "" {
			app.SetUploadDir(opts.UploadDir)
		}
		uiWorker, err := joinWebUIWorker(ctx, app, busControl, listener, cp)
		if err != nil {
			return fmt.Errorf("project: join webui worker: %w", err)
		}
		defer func() { _ = uiWorker.Stop() }()

		if webUIAddr, err = bindServer(opts.WebUIAddr, func(addr string) (string, error) {
			b, err := app.Bind()
			if err != nil {
				return "", err
			}
			url := LocalhostURL(b)
			log.Printf("[%s] WebUI: %s", opts.Banner, url)
			fmt.Printf("%s WebUI listening at %s\n", opts.Banner, url)
			go func() {
				if err := app.Start(ctx); err != nil {
					log.Printf("[%s] webui error: %v", opts.Banner, err)
				}
			}()
			return b, nil
		}); err != nil {
			return fmt.Errorf("project: bind webui: %w", err)
		}
	}
	if opts.OnResolved != nil {
		opts.OnResolved(busAddr, webUIAddr)
	}

	// Process liveness belongs here, not to the control plane: wait for the
	// signal, then let the control plane release what it owns.
	fmt.Printf("%s started. Press Ctrl+C to stop.\n", opts.Banner)
	<-ctx.Done()
	if err := cp.Stop(ctx); err != nil {
		return fmt.Errorf("%s: stop: %w", opts.Banner, err)
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

// killPortHolders kills every process LISTENING on a TCP port, via lsof.
func killPortHolders(port string) {
	_ = signalPortHolders(port, os.Kill)
}

// signalPortHolders sends sig to every process LISTENING on the TCP port (via
// lsof) and returns how many were signalled. Only listeners are matched: a
// plain `-i tcp:<port>` would also match any client connected to that port (e.g.
// the control plane proxying a project), and we must never signal those. pid 1
// is never touched.
func signalPortHolders(port string, sig os.Signal) int {
	out, err := exec.Command("lsof", "-ti", "tcp:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		pidStr := strings.TrimSpace(line)
		if pidStr == "" {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil || pid <= 1 { // never signal init/pid 1
			continue
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		log.Printf("[project] signalling pid %d holding port %s", pid, port)
		if p.Signal(sig) == nil {
			n++
		}
	}
	return n
}

// InterruptPortHolders sends SIGINT to every process listening on port — how a
// project started manually from the CLI (outside the control plane) is stopped
// gracefully: 'niq project run' snapshots on SIGINT, exactly like a
// control-plane stop. Returns the number of processes signalled (0 = nothing
// was listening, i.e. not running).
func InterruptPortHolders(port int) int {
	if port <= 0 {
		return 0
	}
	return signalPortHolders(strconv.Itoa(port), os.Interrupt)
}

// bindServer binds a listener at reqAddr via start; if a specific (non-":0")
// address fails to bind (e.g. a stale process still owns the port), it falls
// back to an ephemeral port on the same host so a restarted project still comes
// up. The resolved (possibly fallback) host:port is returned to the caller to be
// persisted.
func bindServer(reqAddr string, start func(addr string) (string, error)) (string, error) {
	resolved, err := start(reqAddr)
	if err == nil || reqAddr == ":0" {
		return resolved, err
	}
	fallback := ephemeralAddr(reqAddr)
	log.Printf("[project] addr %s unavailable (%v); falling back to %s", reqAddr, err, fallback)
	if resolved2, err2 := start(fallback); err2 == nil {
		return resolved2, nil
	}
	return "", err
}

// ephemeralAddr maps reqAddr to port 0 on the same host, so the fallback keeps
// the requested exposure: a loopback bind stays loopback (a project WebUI must
// never widen to all interfaces just because its port was taken), while a
// wildcard or explicit LAN bind keeps that interface.
func ephemeralAddr(reqAddr string) string {
	host, _, err := net.SplitHostPort(reqAddr)
	if err != nil || host == "" || host == "0.0.0.0" || host == "::" {
		return ":0" // already the widest exposure
	}
	return net.JoinHostPort(host, "0")
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

// rootSecretPath is the file holding the project bus's token-signing secret.
const rootSecretPath = "root_secret"

// loadOrCreateRootSecret loads the project's signing secret, generating and
// persisting a fresh random one on first use. The secret anchors the bus's
// single token-issuing authority; persisting it lets long-lived provisioned
// tokens remain verifiable across restarts.
func loadOrCreateRootSecret(dir string) ([]byte, error) {
	if dir == "" {
		return nil, fmt.Errorf("root secret requires a dir")
	}
	p := filepath.Join(dir, rootSecretPath)
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		return nil, err
	}
	return b, nil
}

// joinWebUIWorker brings the app-hosted interface worker onto the bus.
//
// It is the one worker the assembly joins itself instead of materializing from a
// manifest: its backend is a process-local object (the WebUI app), which no
// manifest can describe, so it has no builder and is never spawnable. Its
// identity and grants are declared here, in code — that is also why it needs no
// ACL editing: there is no user-editable grant to persist.
func joinWebUIWorker(
	ctx context.Context,
	app *webui.Server,
	bus corebus.Control,
	listener *inprocess.InProcListener,
	cp *projworker.ControlPlane,
) (*iface.Worker, error) {
	id := projworker.DefaultWebUIID
	identity := corebus.Identity{
		WorkerID: id,
		Type:     projworker.TypeInterface,
		// The human's voice: input, arbitrary worker events, approval decisions,
		// and the replies to its own asks. `*` is required for the "drive a worker
		// with one of its declared events" endpoint, whose type is by definition
		// not enumerable here.
		PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")},
		SubscribeAllow: []event.EventPattern{
			event.NewPattern(event.TypeWorkerInput),
			event.NewPattern(event.TypeApprovalRequest),
			event.NewPattern(event.TypeApprovalDecision),
			event.NewPattern("worker.discover"),
		},
	}
	// Grants are declared in code, so they are authoritative on every start:
	// create the identity on first run, refresh its grants afterwards.
	if _, ok := bus.Lookup(id); ok {
		if err := bus.UpdateACL(id, identity.PublishAllow, identity.SubscribeAllow); err != nil {
			return nil, fmt.Errorf("refresh identity: %w", err)
		}
	} else if err := bus.Register(identity); err != nil {
		return nil, fmt.Errorf("register identity: %w", err)
	}

	// The in-process listener accepts the bus side and attaches it, for this
	// worker exactly as for every other in-process worker.
	ch := inprocess.NewWorkerSide(id, listener)
	if err := ch.Connect(ctx, "inproc://webui"); err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}

	var w *iface.Worker
	// Persist the worker's own state (the approval inbox) through the control
	// plane, which owns runtime state for the project. It is a signal, not a
	// policy: iface says "my state changed", the control plane decides when it
	// lands.
	save := func() {
		if w == nil {
			return
		}
		snap, err := w.Snapshot()
		if err != nil {
			log.Printf("[project] %s: snapshot: %v", id, err)
			return
		}
		if err := cp.SaveState(id, snap); err != nil {
			log.Printf("[project] %s: persist state: %v", id, err)
		}
	}
	w = iface.New(iface.Config{
		ID:       id,
		Type:     projworker.TypeInterface,
		Bus:      ch,
		Backends: []iface.Backend{app},
		// The human answers approvals here: the worker declares the capability in
		// its watch contract and parks requests for the app to render.
		Approvals:       true,
		OnDurableChange: save,
	})
	// Restore BEFORE Start: the worker must come up holding the inbox it had, so a
	// request parked before the restart is still answerable.
	if snap, ok, err := cp.State(id); err != nil {
		return nil, err
	} else if ok {
		if err := w.Restore(snap); err != nil {
			return nil, fmt.Errorf("restore %s: %w", id, err)
		}
	}
	if err := w.Start(ctx); err != nil {
		return nil, err
	}
	return w, nil
}
