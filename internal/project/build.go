package project

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	programpkg "github.com/niq-run/niq/core/program"
	"github.com/niq-run/niq/core/worker"
	providerpkg "github.com/niq-run/niq/internal/project/provider"
	"github.com/niq-run/niq/pkg/eventbus"
	eventbusapi "github.com/niq-run/niq/pkg/eventbus/api"
	"github.com/niq-run/niq/pkg/eventbus/transport/inprocess"
	"github.com/niq-run/niq/pkg/services/pgbackend"
	"github.com/niq-run/niq/pkg/services/workerhost"
	"github.com/niq-run/niq/pkg/services/wsbackend"
	"github.com/niq-run/niq/pkg/workers/hiw"
	"github.com/niq-run/niq/pkg/workers/host"
	programworker "github.com/niq-run/niq/pkg/workers/program"
	"github.com/niq-run/niq/pkg/workers/reason"
	"github.com/niq-run/niq/pkg/workers/timer"
	"github.com/niq-run/niq/pkg/workers/workspace"
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
func specConnect(ctx BuildContext, id, typ string, pubAllow []event.PublishPattern, subAllow []event.EventPattern) func() (corebus.WorkerSideChannel, error) {
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

	// SubscribeAllow: only broadcast delivery needs listing. Directed events
	// (tool calls, their request.* replies, timer.timeout/reminder, management
	// requests) reach the worker regardless; broadcast traffic to a reason
	// worker is just the worker presence lifecycle and worker.input (hiw
	// broadcasts when untargeted). Template subscriptions override this
	// default; a configured subscriptions list REPLACES it entirely.
	subAllow := subAllowFromParams(p, []string{
		"worker.ready", "worker.gone", "worker.discover", "worker.abort",
		"worker.input",
	})
	// PublishAllow default stays "*": the reason worker forwards tool
	// invocations to whichever peers it discovers, under the peers' own event
	// types (ls, timer.timeout, program.query, user extensions...) — a set
	// that is inherently dynamic and cannot be enumerated statically. This is
	// a control-plane grant, not a worker-side declaration: it can be
	// narrowed at runtime via the control plane's allow editing, or
	// statically via params.publish in the worker spec.
	pubAllow := publishPatterns(p["publish"])
	if len(pubAllow) == 0 {
		pubAllow = []event.PublishPattern{event.NewPublishPattern("*")}
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
	providerSources := providerpkg.NewProviderSources(provider, apiKey, baseURL, model)
	pname, pmodel := providerpkg.InitialProviderInfo(provider, apiKey, baseURL, model)
	build := func(ch corebus.WorkerSideChannel) worker.ManagedWorker {
		w := reason.NewWorker(reason.Config{
			ID:               id,
			Provider:         providerSources.Default(),
			ProviderSources:  providerSources,
			ProviderName:     pname,
			ProviderModel:    pmodel,
			Programs:         programs,
			EventConverters:  events,
			Bus:              ch,
			ContextWindow:    contextWindow,
			BudgetSoft:       budgetSoft,
			BudgetHard:       budgetHard,
			KeepTail:         keepTail,
			CompactDirective: compactDirective,
			// Fixed per-message payload cap for tool results / inputs; not
			// configurable through worker params.
			MaxPayloadBytes: 20 * 1024,
			SeedMessages:    seedBrief,
			// A runtime provider switch (worker.update provider.switch) must
			// outlive this process, so the worker signals it and the assembly
			// layer checkpoints it. The worker builds its provider from
			// provider.json, which it knows nothing about — persistence is
			// the host's job, not the mechanism's.
			OnDurableChange: func() {
				if err := ctx.WorkerSvc.Checkpoint(id); err != nil {
					log.Printf("[project] checkpoint %s: %v", id, err)
				}
			},
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
	// Use the id passed in from the project/config definition. Only fall back to
	// a path-derived id when none was given (legacy bare workspace).
	id := cfg.ID
	if id == "" {
		id = "ws-" + sanitizeWorkerID(abs)
	}
	params := p
	params["root_dir"] = abs
	cfg.ID = id
	cfg.Params = params

	// PublishAllow: the workspace replies to tool calls (request.*) and
	// announces presence. SubscribeAllow: empty — tool calls arrive directed;
	// the workspace consumes no broadcasts. Not driven by stale
	// params.subscriptions (e.g. an old tool.requested).
	connect := specConnect(ctx, id, "workspace",
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
		},
		nil)
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
	// PublishAllow: lifecycle replies (request.*) and presence. Its tool
	// events (spawn/suspend/resume) are directed calls — no subscription
	// needed; request.cancel is directed too (sent to the target worker).
	connect := specConnect(ctx, id, "host",
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
			event.NewPublishPattern("worker.discover"),
		},
		nil)
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
	// PublishAllow: replies, the directed fires back to the caller, presence.
	// Subscribes to nothing — everything it receives is directed.
	connect := specConnect(ctx, id, "timer",
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("timer.timeout"),
			event.NewPublishPattern("timer.reminder"),
			event.NewPublishPattern("worker.ready"),
		},
		nil)
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
		id = "webui-hiw"
	}
	// PublishAllow: a `*` grant so the human UI (acting as HIW) can send a
	// worker any event in its "watch" contract, driving its behaviour/state on
	// the bus through the normal ACL path. Subscribes to nothing.
	connect := specConnect(ctx, id, "hiw",
		[]event.PublishPattern{
			event.NewPublishPattern("*"),
		},
		nil)
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

	// PublishAllow: replies and presence. Subscribes to nothing — program
	// query/update arrive directed.
	connect := specConnect(ctx, id, "program",
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
		},
		nil)
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

func eventPatternsFromStrings(types []string) []event.EventPattern {
	out := make([]event.EventPattern, 0, len(types))
	for _, t := range types {
		out = append(out, event.NewPattern(event.EventType(t)))
	}
	return out
}

// subscriptionPatterns parses config subscription entries into bus patterns.
// Each entry is a bare event-type string or a {"type","source"} object (see
// SubscriptionSpec); both spell the same EventPattern.
func subscriptionPatterns(v any) []event.EventPattern {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]event.EventPattern, 0, len(raw))
	for _, r := range raw {
		switch e := r.(type) {
		case string:
			out = append(out, event.NewPattern(event.EventType(e)))
		case map[string]any:
			t, _ := e["type"].(string)
			if t == "" {
				continue
			}
			s, _ := e["source"].(string)
			out = append(out, event.EventPattern{Type: event.EventType(t), SourceID: s})
		}
	}
	return out
}

// publishPatterns parses config publish entries into bus publish grants.
// Each entry is a bare event-type string or a {"type","target"} object (see
// PublishSpec); both spell the same PublishPattern.
func publishPatterns(v any) []event.PublishPattern {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return nil
	}
	out := make([]event.PublishPattern, 0, len(raw))
	for _, r := range raw {
		switch e := r.(type) {
		case string:
			out = append(out, event.NewPublishPattern(event.EventType(e)))
		case map[string]any:
			t, _ := e["type"].(string)
			if t == "" {
				continue
			}
			tgt, _ := e["target"].(string)
			out = append(out, event.PublishPattern{Type: event.EventType(t), Target: tgt})
		}
	}
	return out
}

// subAllowFromParams resolves a worker's SubscribeAllow (the broadcast
// delivery whitelist) from the config's subscriptions, falling back to the
// given hardcoded defaults when none are configured. The template value and
// the defaults are both only the initial grant: the control plane can edit
// SubscribeAllow at runtime via the registry API. Note SubscribeAllow only
// gates Broadcast delivery — directed tool calls always reach their target
// regardless of it, so tool capability events belong in the worker's own
// declarations, not here.
func subAllowFromParams(p map[string]any, defaults []string) []event.EventPattern {
	if sub := subscriptionPatterns(p["subscriptions"]); len(sub) > 0 {
		return sub
	}
	return eventPatternsFromStrings(defaults)
}

func sanitizeWorkerID(path string) string {
	s := strings.TrimPrefix(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}
