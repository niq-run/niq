package swarm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	corebus "github.com/54c1/niq/core/bus"
	"github.com/54c1/niq/core/event"
	"github.com/54c1/niq/core/llm"
	programpkg "github.com/54c1/niq/core/program"
	"github.com/54c1/niq/core/worker"
	"github.com/54c1/niq/ext/service/pgbackend"
	"github.com/54c1/niq/ext/service/wsbackend"
	"github.com/54c1/niq/ext/worker/workspace"
	"github.com/54c1/niq/pkg/providercfg"
	"github.com/54c1/niq/pkg/service/eventbus"
	eventbusapi "github.com/54c1/niq/pkg/service/eventbus/api"
	"github.com/54c1/niq/pkg/service/eventbus/transport/inprocess"
	"github.com/54c1/niq/pkg/service/workerhost"
	"github.com/54c1/niq/pkg/worker/hiw"
	"github.com/54c1/niq/pkg/worker/host"
	programworker "github.com/54c1/niq/pkg/worker/program"
	"github.com/54c1/niq/pkg/worker/reason"
	"github.com/54c1/niq/pkg/worker/timer"
)

// BuildContext holds shared dependencies that worker builders need.
type BuildContext struct {
	Registry     corebus.IdentityRegistry
	Listener     *inprocess.InProcListener
	Engine       *eventbus.Engine
	WorkerSvc    *workerhost.WorkerService
	EventLog     *eventbusapi.EventLog
	ProgramsRoot string
}

// RegisterBuilders registers a Builder for every known worker type onto service.
func RegisterBuilders(ctx BuildContext, svc *workerhost.WorkerService) {
	svc.RegisterBuilder("reason", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return buildReasonSpec(ctx, cfg)
	})
	svc.RegisterBuilder("workspace", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return buildWorkspaceSpec(ctx, cfg)
	})
	svc.RegisterBuilder("host", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return buildHostSpec(ctx, cfg)
	})
	svc.RegisterBuilder("timer", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return buildTimerSpec(ctx, cfg)
	})
	svc.RegisterBuilder("hiw", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return buildHIWSpec(ctx, cfg)
	})
	svc.RegisterBuilder("program", func(cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
		return buildProgramSpec(ctx, cfg)
	})
}

// specConnect builds a Connect closure: registers the identity idempotently and
// creates a fresh, connected in-process worker-side channel.
func specConnect(ctx BuildContext, id, typ string, pubAllow []string, subAllow []event.EventPattern) func() (corebus.WorkerSideChannel, error) {
	return func() (corebus.WorkerSideChannel, error) {
		if err := ctx.Registry.Register(corebus.Identity{
			WorkerID:       id,
			Type:           typ,
			PublishAllow:   pubAllow,
			SubscribeAllow: subAllow,
		}); err != nil {
			// Identity may exist from a previous run; refresh its allow lists so
			// they reflect the current builder config.
			if strings.Contains(err.Error(), "already registered") {
				ctx.Registry.Update(id, pubAllow, subAllow)
			} else {
				return nil, err
			}
		}
		ch := inprocess.NewWorkerSide(id, ctx.Listener)
		if err := ch.Connect(context.Background(), "inproc://niq"); err != nil {
			return nil, err
		}
		return ch, nil
	}
}

// ── reason ──

func buildReasonSpec(ctx BuildContext, cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	id := cfg.ID
	if id == "" {
		return worker.SpawnSpec{}, fmt.Errorf("reason: id is required")
	}
	p := cfg.Params

	provider, _ := p["provider"].(string)
	apiKey, _ := p["api_key"].(string)
	baseURL, _ := p["base_url"].(string)
	model, _ := p["model"].(string)

	subAllow := eventPatternsFromStrings(stringSlice(p["subscriptions"]))
	if len(subAllow) == 0 {
		for _, t := range []string{
			"tool.completed", "tool.failed", "tool.rejected",
			"worker.ready", "worker.gone", "worker.discover", "worker.abort",
			"timer.timeout", "timer.reminder", "worker.input", "tool.requested",
		} {
			subAllow = append(subAllow, event.NewPattern(event.EventType(t)))
		}
	}
	pubAllow := stringSlice(p["publish"])
	if len(pubAllow) == 0 {
		pubAllow = []string{"*"}
	}
	programs := parsePrograms(p, id)
	events := parseEvents(p)

	// Spawn seeding (context-builder.md §6): goal lands in the system prompt
	// (program space, survives compaction); brief lands as the transcript's
	// first message (working material, compactable). The two must stay
	// separate or compaction threatens the goal itself.
	goal, _ := p["goal"].(string)
	brief, _ := p["brief"].(string)
	if goal != "" {
		programs = append(programSeed(goal), programs...)
	}
	var seedBrief []llm.Message
	if brief != "" {
		seedBrief = []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentBlock{{Type: llm.ContentText,
				Text: "[handover brief from spawner]\n" + brief}},
		}}
	}

	// Context budget params (optional; defaults live in the reason package).
	contextWindow, _ := p["context_window"].(int)
	budgetSoft, _ := p["budget_soft"].(float64)
	budgetHard, _ := p["budget_hard"].(float64)
	keepTail, _ := p["keep_tail"].(int)
	compactDirective, _ := p["compact_directive"].(string)

	connect := specConnect(ctx, id, "reason", pubAllow, subAllow)
	providerSources := newSwarmProviderSources(provider, apiKey, baseURL, model)
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		w := reason.NewWorker(reason.Config{
			ID:               id,
			Provider:         providerSources.Default(),
			ProviderSources:  providerSources,
			Programs:         programs,
			EventConverters:  events,
			Bus:              ch,
			ContextWindow:    contextWindow,
			BudgetSoft:       budgetSoft,
			BudgetHard:       budgetHard,
			KeepTail:         keepTail,
			CompactDirective: compactDirective,
			SeedMessages:     seedBrief,
		})
		return w
	}
	cfg.Type = "reason"
	return worker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── workspace ──

func buildWorkspaceSpec(ctx BuildContext, cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	p := cfg.Params
	path, _ := p["root_dir"].(string)
	if path == "" {
		path, _ = p["path"].(string)
	}
	if path == "" {
		return worker.SpawnSpec{}, fmt.Errorf("workspace: root_dir is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return worker.SpawnSpec{}, fmt.Errorf("workspace: bad root_dir: %w", err)
	}
	id := "ws-" + sanitizeWorkerID(abs)
	params := p
	params["root_dir"] = abs
	cfg.ID = id
	cfg.Params = params

	connect := specConnect(ctx, id, "workspace", []string{"*"}, []event.EventPattern{
		event.NewPattern(event.TypeToolRequested),
		event.NewPattern(event.TypeWorkerDiscover),
	})
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		return workspace.New(workspace.Config{
			ID:      id,
			Bus:     ch,
			Backend: wsbackend.NewEmbeddedBackend(abs),
		})
	}
	cfg.Type = "workspace"
	return worker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── host ──

func buildHostSpec(ctx BuildContext, cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	id := cfg.ID
	if id == "" {
		id = "host"
	}
	connect := specConnect(ctx, id, "host", []string{"*"}, []event.EventPattern{
		event.NewPattern(event.TypeToolRequested),
		event.NewPattern(event.TypeToolCancel),
		event.NewPattern(event.TypeWorkerDiscover),
	})
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		return host.New(host.Config{ID: id, Bus: ch, Engine: ctx.WorkerSvc})
	}
	cfg.ID = id
	cfg.Type = "host"
	return worker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── timer ──

func buildTimerSpec(ctx BuildContext, cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	id := cfg.ID
	if id == "" {
		id = "timer"
	}
	connect := specConnect(ctx, id, "timer", []string{"*"}, []event.EventPattern{
		event.NewPattern(event.TypeToolRequested),
		event.NewPattern(event.TypeWorkerDiscover),
	})
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		return timer.New(timer.Config{ID: id, Bus: ch})
	}
	cfg.ID = id
	cfg.Type = "timer"
	return worker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── hiw ──

func buildHIWSpec(ctx BuildContext, cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	id := cfg.ID
	if id == "" {
		id = "default-hiw"
	}
	connect := specConnect(ctx, id, "hiw", []string{"*"}, []event.EventPattern{event.NewPattern("*")})
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		return hiw.New(hiw.Config{ID: id, Bus: ch})
	}
	cfg.ID = id
	cfg.Type = "hiw"
	return worker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── program ──

func buildProgramSpec(ctx BuildContext, cfg worker.WorkerConfig) (worker.SpawnSpec, error) {
	id := cfg.ID
	if id == "" {
		id = "program"
	}
	root, _ := cfg.Params["root_dir"].(string)
	if root == "" {
		root = ctx.ProgramsRoot
	}
	if root == "" {
		home, _ := os.UserHomeDir()
		root = filepath.Join(home, ".niq", "programs")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return worker.SpawnSpec{}, fmt.Errorf("program: bad root_dir: %w", err)
	}
	os.MkdirAll(abs, 0755)

	connect := specConnect(ctx, id, "program", []string{"*"}, []event.EventPattern{
		event.NewPattern(event.TypeToolRequested),
		event.NewPattern(event.TypeWorkerDiscover),
	})
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		return programworker.New(programworker.Config{
			ID:      id,
			Bus:     ch,
			Backend: pgbackend.New(abs),
		})
	}
	cfg.ID = id
	cfg.Type = "program"
	return worker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── helpers ──

func providerFromArgs(provider, apiKey, baseURL, model string) llm.LLMProvider {
	if provider != "" {
		if p, ok := providercfg.Find(provider); ok {
			return providercfg.BuildWithOverrides(p, apiKey, baseURL, model)
		}
		if p, ok := providercfg.FindByType(provider); ok {
			return providercfg.BuildWithOverrides(p, apiKey, baseURL, model)
		}
		return providercfg.Build(providercfg.Provider{
			Type:    provider,
			APIKey:  apiKey,
			BaseURL: baseURL,
			Model:   model,
		})
	}
	if p, ok := providercfg.Default(); ok {
		return providercfg.BuildWithOverrides(p, apiKey, baseURL, model)
	}
	return providercfg.Build(providercfg.Provider{
		Type:    "deepseek",
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	})
}

// programSeed builds the goal instruction program for a spawned reason
// worker. The goal lives in program space: rendered into the system prompt
// every round, never compacted away.
func programSeed(goal string) []programpkg.Program {
	return []programpkg.Program{{
		Meta: programpkg.Meta{
			Name:        "goal",
			ContentType: programpkg.ContentTypeInstruction,
		},
		EntryContent: programpkg.ProgramContent{Content: "# Goal\n\n" + goal},
	}}
}

// parsePrograms extracts a simplified program list from spawn params.
func parsePrograms(p map[string]any, workerID string) []programpkg.Program {
	raw, ok := p["programs"].([]any)
	if !ok || len(raw) == 0 {
		// A default instruction program derived from the instruction text.
		if instr, _ := p["instruction"].(string); instr != "" {
			return []programpkg.Program{
				{
					Meta: programpkg.Meta{
						Name:        workerID + "-instruction",
						ContentType: programpkg.ContentTypeInstruction,
					},
					EntryContent: programpkg.ProgramContent{Content: instr},
				},
			}
		}
		return []programpkg.Program{
			{
				Meta: programpkg.Meta{
					Name:        workerID + "-instruction",
					ContentType: programpkg.ContentTypeInstruction,
				},
			},
		}
	}

	progs := make([]programpkg.Program, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		ctStr, _ := m["content_type"].(string)
		if name == "" || ctStr == "" {
			continue
		}
		var ct programpkg.ContentType
		switch ctStr {
		case "instruction":
			ct = programpkg.ContentTypeInstruction
		case "playbook":
			ct = programpkg.ContentTypePlaybook
		default:
			continue
		}
		desc, _ := m["description"].(string)
		content, _ := m["content"].(string)
		progs = append(progs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        name,
				ContentType: ct,
				Description: desc,
			},
			EntryContent: programpkg.ProgramContent{Content: content},
		})
	}
	if len(progs) == 0 {
		progs = append(progs, programpkg.Program{
			Meta: programpkg.Meta{
				Name:        workerID + "-instruction",
				ContentType: programpkg.ContentTypeInstruction,
			},
		})
	}
	return progs
}

// parseEvents extracts event type subscriptions from spawn params.
func parseEvents(p map[string]any) []reason.EventConverter {
	raw, ok := p["events"].([]any)
	if !ok {
		return nil
	}
	handlers := make([]reason.EventConverter, 0, len(raw))
	for _, r := range raw {
		evtType, ok := r.(string)
		if !ok || evtType == "" {
			continue
		}
		handlers = append(handlers, reason.EventConverter{
			Pattern:   event.NewPattern(event.EventType(evtType)),
			Converter: reason.DefaultConverter,
		})
	}
	return handlers
}

func stringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		if s, ok := r.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// eventPatternsFromStrings converts type-only subscription strings to patterns.
func eventPatternsFromStrings(types []string) []event.EventPattern {
	out := make([]event.EventPattern, 0, len(types))
	for _, t := range types {
		out = append(out, event.NewPattern(event.EventType(t)))
	}
	return out
}

func sanitizeWorkerID(path string) string {
	s := strings.TrimPrefix(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
