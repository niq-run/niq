package swarm

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/internal/swarm/webui"
	"github.com/54c1/niq/pkg/service/eventbus"
	eventbusapi "github.com/54c1/niq/pkg/service/eventbus/api"
	"github.com/54c1/niq/pkg/service/eventbus/transport/httptrans"
	"github.com/54c1/niq/pkg/service/eventbus/transport/inprocess"
	"github.com/54c1/niq/pkg/service/workerhost"
	"github.com/54c1/niq/pkg/worker/hiw"
)

// RunOptions controls the swarm command's behaviour.
type RunOptions struct {
	ConfigPath   string // --config
	Preset       string // --preset
	WebUIAddr    string // --webui
	ProgramsRoot string // --programs-root
	StateDir     string // state root for worker persistence (default ~/.niq/state/workers)
}

// RunSwarm resolves a config (template/--config) and runs a single swarm in the
// shared, non-project "control" layout.
func RunSwarm(opts RunOptions) error {
	// Parse config from the shared templates dir (seeded from the built-ins on
	// first run) or an explicit --config file.
	homeDir, _ := os.UserHomeDir()
	templatesDir := filepath.Join(homeDir, ".niq", "common", "templates")
	if err := SeedTemplates(templatesDir); err != nil {
		log.Printf("[swarm] seed templates: %v", err)
	}

	var cfg *SwarmConfig
	var err error
	switch {
	case opts.ConfigPath != "":
		cfg, err = ParseConfig(opts.ConfigPath)
	case opts.Preset != "":
		cfg, err = LoadTemplate(templatesDir, opts.Preset)
	default:
		cfg, err = LoadTemplate(templatesDir, "dev")
	}
	if err != nil {
		return err
	}
	if opts.StateDir == "" {
		opts.StateDir = filepath.Join(homeDir, ".niq", "state", "workers")
	}

	return runAssembly(cfg, assemblyOptions{
		IDDir:        filepath.Join(homeDir, ".niq", "id"),
		StateDir:     opts.StateDir,
		ProgramsRoot: opts.ProgramsRoot,
		WebUIAddr:    opts.WebUIAddr,
		Banner:       "niq swarm",
	})
}

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

	busAddr := opts.BusAddr
	if busAddr == "" && p.Ports.Bus != 0 {
		busAddr = fmt.Sprintf("127.0.0.1:%d", p.Ports.Bus)
	}
	if busAddr == "" {
		busAddr = ":0"
	}
	webUIAddr := opts.WebUIAddr
	if webUIAddr == "" && p.Ports.WebUI != 0 {
		webUIAddr = fmt.Sprintf("127.0.0.1:%d", p.Ports.WebUI)
	}
	if webUIAddr == "" {
		webUIAddr = ":0"
	}

	onResolved := func(bus, webui string) {
		if p.Ports.Bus = portOf(bus); p.Ports.Bus == 0 {
			p.Ports.Bus = resolvePort(busAddr)
		}
		if p.Ports.WebUI = portOf(webui); p.Ports.WebUI == 0 {
			p.Ports.WebUI = resolvePort(webUIAddr)
		}
		if err := SaveProject(p); err != nil {
			log.Printf("[project %s] persist ports: %v", opts.ProjectID, err)
		}
	}

	return runAssembly(&SwarmConfig{Workers: p.Workers}, assemblyOptions{
		IDDir:        filepath.Join(projDir, "id"),
		StateDir:     filepath.Join(projDir, "state", "workers"),
		ProgramsRoot: filepath.Join(projDir, "programs"),
		BusAddr:      busAddr,
		WebUIAddr:    webUIAddr,
		Banner:       "project " + opts.ProjectID,
		NoBrowser:    true, // the control WebUI drives the redirect, not this process
		OnResolved:   onResolved,
	})
}

// assemblyOptions parameterizes runAssembly: where the bus/identities/workers
// live (shared control vs per-project) and which network services to expose.
type assemblyOptions struct {
	IDDir        string
	StateDir     string
	ProgramsRoot string
	BusAddr      string // "" disables the HTTP bus (legacy control swarm)
	WebUIAddr    string // "" disables the WebUI
	Banner       string
	NoBrowser    bool
	OnResolved   func(bus, webui string)
}

// runAssembly is the shared core: build the bus, host the workers from cfg, and
// (optionally) expose an HTTP transport bus and a WebUI. Used by RunSwarm
// (control layout) and RunProject (per-project layout).
func runAssembly(cfg *SwarmConfig, opts assemblyOptions) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Identity registry (file-backed).
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(opts.IDDir, "identities.json"))
	if err != nil {
		return fmt.Errorf("swarm: create registry: %w", err)
	}

	// Event bus engine with in-memory event store.
	evtStore := eventbus.NewMemoryEventStore()
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
	store, err := workerhost.NewFileWorkerStore(opts.StateDir)
	if err != nil {
		return fmt.Errorf("swarm: create worker store: %w", err)
	}
	workerSvc.SetStore(store)

	// Optional HTTP transport bus (for external/three-party workers).
	var busAddr string
	if opts.BusAddr != "" {
		busSrv := httptrans.NewServer(engine, registry, opts.BusAddr)
		if busAddr, err = busSrv.Bind(); err != nil {
			return fmt.Errorf("swarm: bind bus: %w", err)
		}
		go func() {
			if err := busSrv.Start(ctx); err != nil {
				log.Printf("[swarm] httptrans error: %v", err)
			}
		}()
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

	// Create workers. Recovery is persisted-authoritative: a declared worker
	// with a persisted snapshot is restored from that snapshot (params + state)
	// rather than rebuilt fresh, so an existing project resumes exactly where it
	// left off. A declared worker with no persisted state is created fresh from
	// its definition (first run / new worker).
	var hiwID string
	declared := map[string]bool{}
	recByID := map[string]workerhost.WorkerRecord{}
	if recs, err := workerSvc.LoadAllWorkers(); err == nil {
		for _, rec := range recs {
			recByID[rec.ID] = rec
		}
	}
	for _, wc := range cfg.Workers {
		declared[wc.ID] = true
		wcfg := func() worker.WorkerConfig { // fresh-create from declared definition
			return worker.WorkerConfig{ID: wc.ID, Type: wc.Type, Params: workerConfigParams(wc)}
		}
		if rec, ok := recByID[wc.ID]; ok && len(rec.Snapshot) > 0 {
			log.Printf("[swarm] recovering worker: %s (type=%s) from persisted state", wc.ID, wc.Type)
			if err := workerSvc.RestoreAndRun(ctx,
				worker.WorkerConfig{ID: rec.ID, Type: rec.Type, Params: rec.Params}, rec.Snapshot); err != nil {
				log.Printf("[swarm] restore worker %s: %v; falling back to fresh create", wc.ID, err)
				if err := workerSvc.CreateWorker(ctx, wcfg()); err != nil {
					return fmt.Errorf("swarm: create worker %q: %w", wc.ID, err)
				}
			}
		} else {
			log.Printf("[swarm] creating worker: %s (type=%s)", wc.ID, wc.Type)
			if err := workerSvc.CreateWorker(ctx, wcfg()); err != nil {
				return fmt.Errorf("swarm: create worker %q: %w", wc.ID, err)
			}
		}
		if wc.Type == "hiw" {
			hiwID = wc.ID
		}
	}

	// Bootstrap persisted spawned workers not declared in config, as suspended.
	if err := bootstrapPersisted(buildCtx, workerSvc, cfg); err != nil {
		log.Printf("[swarm] worker bootstrap: %v", err)
	}

	// Optional WebUI, served on the project/control address.
	var webUIAddr string
	if opts.WebUIAddr != "" && hiwID != "" {
		if h, ok := workerSvc.Worker(hiwID); ok {
			if hiwWorker, ok := h.(*hiw.Worker); ok {
				s := webui.New(hiwWorker, eventLog, engine, workerSvc, registry, opts.WebUIAddr, false)
				if webUIAddr, err = s.Bind(); err != nil {
					return fmt.Errorf("swarm: bind webui: %w", err)
				}
				url := localhostURL(webUIAddr)
				log.Printf("[%s] WebUI: %s", opts.Banner, url)
				fmt.Printf("%s WebUI listening at %s\n", opts.Banner, url)
				go func() {
					if err := s.Start(ctx); err != nil {
						log.Printf("[%s] webui error: %v", opts.Banner, err)
					}
				}()
				if !opts.NoBrowser {
					time.Sleep(400 * time.Millisecond)
					_ = openBrowser(url)
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
		return fmt.Errorf("%s: %w", opts.Banner, err)
	}
	fmt.Printf("\n%s stopped.\n", opts.Banner)
	return nil
}

// workerConfigParams converts a WorkerConfig into the Params map consumed by
// the builders.
func workerConfigParams(wc WorkerConfig) map[string]any {
	p := map[string]any{}
	if wc.Instruction != "" {
		p["instruction"] = wc.Instruction
	}
	if wc.Provider != "" {
		p["provider"] = wc.Provider
	}
	if wc.APIKey != "" {
		p["api_key"] = wc.APIKey
	}
	if wc.BaseURL != "" {
		p["base_url"] = wc.BaseURL
	}
	if wc.Model != "" {
		p["model"] = wc.Model
	}
	if len(wc.Subscriptions) > 0 {
		arr := make([]any, len(wc.Subscriptions))
		for i, s := range wc.Subscriptions {
			arr[i] = s
		}
		p["subscriptions"] = arr
	}
	if len(wc.Publish) > 0 {
		arr := make([]any, len(wc.Publish))
		for i, s := range wc.Publish {
			arr[i] = s
		}
		p["publish"] = arr
	}
	if wc.RootDir != "" {
		p["root_dir"] = wc.RootDir
	}
	return p
}

// localhostURL turns a resolved listen address (e.g. "[::]:19763") into a
// clickable localhost URL.
func localhostURL(resolved string) string {
	if resolved == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(resolved); err == nil {
		return "http://localhost:" + port
	}
	return "http://" + resolved
}

// portOf parses the numeric port from a host:port address, 0 on failure.
func portOf(addr string) int {
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
	if n := portOf(addr); n != 0 {
		return n
	}
	trimmed := strings.TrimPrefix(addr, ":")
	if n, err := strconv.Atoi(trimmed); err == nil {
		return n
	}
	return 0
}

// openBrowser opens the given URL in the user's default browser. It is
// best-effort: the child process is launched detached, and any failure is
// returned to the caller for logging rather than aborting the swarm.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// bootstrapPersisted re-materializes spawned workers persisted by a previous
// run that are not declared in the current config, leaving them suspended.
func bootstrapPersisted(ctx BuildContext, svc *workerhost.WorkerService, cfg *SwarmConfig) error {
	recs, err := svc.LoadAllWorkers()
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, wc := range cfg.Workers {
		declared[wc.ID] = true
	}
	for _, rec := range recs {
		if declared[rec.ID] {
			continue
		}
		if svc.HasWorker(rec.ID) {
			continue
		}
		if err := svc.RestoreSuspended(worker.WorkerConfig{
			ID:     rec.ID,
			Type:   rec.Type,
			Params: rec.Params,
		}); err != nil {
			log.Printf("[swarm] restore worker %s: %v", rec.ID, err)
		}
	}
	return nil
}
