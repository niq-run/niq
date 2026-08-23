package swarm

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/internal/swarm/webui"
	"github.com/54c1/niq/pkg/service/eventbus"
	eventbusapi "github.com/54c1/niq/pkg/service/eventbus/api"
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

// RunSwarm is the core entry point for the `niq swarm` command.
// It parses the config, creates the event bus, builds workers, and manages their lifecycle.
func RunSwarm(opts RunOptions) error {
	// 1. Parse config.
	var cfg *SwarmConfig
	var err error
	switch {
	case opts.ConfigPath != "":
		cfg, err = ParseConfig(opts.ConfigPath)
	case opts.Preset != "":
		cfg, err = LoadPreset(opts.Preset)
	default:
		cfg, err = LoadPreset("dev")
	}
	if err != nil {
		return err
	}

	// 2. Create identity registry (file-backed).
	homeDir, _ := os.UserHomeDir()
	idDir := filepath.Join(homeDir, ".niq", "id")
	registry, err := eventbus.NewFileIdentityRegistry(filepath.Join(idDir, "identities.json"))
	if err != nil {
		return fmt.Errorf("swarm: create registry: %w", err)
	}

	// 3. Create event bus engine with in-memory event store.
	evtStore := eventbus.NewMemoryEventStore()
	engine := eventbus.NewEngine(registry, evtStore)

	// 4. Create EventLog and hook it to the engine so every routed event
	// (both Send and Broadcast) is pushed to SSE subscribers.
	eventLog := eventbusapi.NewEventLog(engine, evtStore)
	engine.OnEvent(eventLog.Hook())

	// 5. Create in-process listener and start accepting connections.
	listener := inprocess.NewInProcListener()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	go func() {
		for {
			ch, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			eventbus.Attach(ctx, engine, ch.WorkerID(), ch)
		}
	}()

	// 6. Create WorkerService (control-plane lifecycle manager) and its store.
	workerSvc := workerhost.New()
	stateRoot := opts.StateDir
	if stateRoot == "" {
		stateRoot = filepath.Join(homeDir, ".niq", "state", "workers")
	}
	store, err := workerhost.NewFileWorkerStore(stateRoot)
	if err != nil {
		return fmt.Errorf("swarm: create worker store: %w", err)
	}
	workerSvc.SetStore(store)

	// 7. Build build context and register builders.
	buildCtx := BuildContext{
		Registry:     registry,
		Listener:     listener,
		Engine:       engine,
		WorkerSvc:    workerSvc,
		EventLog:     eventLog,
		ProgramsRoot: opts.ProgramsRoot,
	}
	RegisterBuilders(buildCtx, workerSvc)

	// 8. Create workers from config.
	var hiwID string
	for _, wc := range cfg.Workers {
		log.Printf("[swarm] creating worker: %s (type=%s)", wc.ID, wc.Type)
		wcfg := worker.WorkerConfig{ID: wc.ID, Type: wc.Type, Params: workerConfigParams(wc)}
		if err := workerSvc.CreateWorker(ctx, wcfg); err != nil {
			return fmt.Errorf("swarm: create worker %q: %w", wc.ID, err)
		}
		if wc.Type == "hiw" {
			hiwID = wc.ID
		}
	}

	// 9. Bootstrap persisted spawned workers not declared in config, as suspended.
	if err := bootstrapPersisted(buildCtx, workerSvc, cfg); err != nil {
		log.Printf("[swarm] worker bootstrap: %v", err)
	}

	// 10. Start WebUI server if configured, and try to open it in the browser.
	if opts.WebUIAddr != "" && hiwID != "" {
		var hiwWorker *hiw.Worker
		if w, ok := workerSvc.Worker(hiwID); ok {
			if h, ok := w.(*hiw.Worker); ok {
				hiwWorker = h
			}
		}
		if hiwWorker != nil {
			url := webuiURL(opts.WebUIAddr)
			log.Printf("[swarm] WebUI: %s", url)
			fmt.Printf("WebUI listening at %s\n", url)
			s := webui.New(hiwWorker, eventLog, engine, workerSvc, registry, opts.WebUIAddr, false)
			go func() {
				if err := s.Start(ctx); err != nil {
					log.Printf("[swarm] webui error: %v", err)
				}
			}()
			// Give the listener a moment to bind before opening the browser.
			go func() {
				time.Sleep(400 * time.Millisecond)
				if err := openBrowser(url); err != nil {
					log.Printf("[swarm] open browser: %v", err)
				}
			}()
		} else {
			log.Printf("[swarm] webui: hiw worker %q not running; skipping WebUI", hiwID)
		}
	}

	// 11. Run — blocks until ctx is cancelled. On shutdown, ShutdownSnapshot
	// persists the latest state before workers are stopped.
	fmt.Println("niq swarm started. Press Ctrl+C to stop.")
	if err := workerSvc.Run(ctx); err != nil && err != context.Canceled {
		return fmt.Errorf("swarm: %w", err)
	}

	fmt.Println("\nniq swarm stopped.")
	return nil
}

// workerConfigParams converts a YAML WorkerConfig into the Params map consumed
// by the builders.
// webuiURL turns a listen address (":19763", "127.0.0.1:8080") into a
// clickable URL for the startup banner. 0.0.0.0 maps to localhost.
func webuiURL(addr string) string {
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "http://localhost:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if !strings.Contains(addr, "://") {
		return "http://" + addr
	}
	return addr
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
		// Linux and other unix-like systems.
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

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
