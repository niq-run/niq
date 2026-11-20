// The builders: one constructor per worker type, plus the registry and the
// shared inputs every constructor needs.
//
// A builder turns a declaration's flat params into the SpawnSpec the worker
// service starts: it wires the worker's own dependencies (bus channel, mounts,
// programs root, lifecycle backend, remote handshake), its bus grants, and the
// in-process worker instance. The reason family shares one construction path
// (provider resolution, program/event seeding, budgets); every other type is
// built by its own function below.
package worker

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/app/standalone/home"
	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	projprocess "github.com/niq-run/niq/app/standalone/worker/process"
	wsbackend "github.com/niq-run/niq/core/impl/embedws"
	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/iface"
	reasonbase "github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/impl/workerlife"
	"github.com/niq-run/niq/core/impl/workers/coding"
	"github.com/niq-run/niq/core/impl/workers/directory"
	"github.com/niq-run/niq/core/impl/workers/history"
	"github.com/niq-run/niq/core/impl/workers/host"
	"github.com/niq-run/niq/core/impl/workers/mcp"
	"github.com/niq-run/niq/core/impl/workers/niq"
	programworker "github.com/niq-run/niq/core/impl/workers/program"
	"github.com/niq-run/niq/core/impl/workers/program/pgbackend"
	"github.com/niq-run/niq/core/impl/workers/program/quickjsexec"
	"github.com/niq-run/niq/core/impl/workers/reason"
	"github.com/niq-run/niq/core/impl/workers/timer"
	"github.com/niq-run/niq/core/impl/workers/workspace"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
	programpkg "github.com/niq-run/niq/core/itfs/program"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// The control-plane workers every project ensures. They are named here because
// the builders default to them, and the assembly references them by these
// constants instead of repeating literals.
const (
	// DefaultWebUIID is the app-hosted interface worker the WebUI drives. It has
	// no declaration and no builder: the assembly joins it directly.
	DefaultWebUIID = "webui"
	// TypeInterface is the type label of an interface worker: a worker whose
	// backend is an external interface, injected by the assembly. It is not
	// spawnable — no backend means nothing to bridge to — so it has no builder.
	TypeInterface = "interface"
	// DefaultHostWorkerID is the lifecycle control plane.
	DefaultHostWorkerID = "host"
)

// BuildContext holds shared dependencies that worker builders need.
type BuildContext struct {
	Registry     corebus.IdentityRegistry
	Bus          corebus.Control
	Listener     *inprocess.InProcListener
	Engine       *eventbus.Engine
	Manager      *workerlife.Manager
	EventLog     *eventbusapi.EventLog
	EventStore   event.EventStore
	ProgramsRoot string
	// StateDir is the per-project workers state directory (`<proj>/workers`);
	// built-in workers persist their own state here (one dir per id).
	StateDir string
	// WorkerBackend is the control plane the host worker drives: the same
	// instance the assembly uses for recovery and shutdown.
	WorkerBackend coreworker.Backend
}

// registerBuilders registers one builder per known worker type, each with its
// spawn-parameter check.
func registerBuilders(ctx BuildContext, types *typeRegistry) {
	// Each type's spawn-parameter check is registered with its builder: the
	// builder is where the valid parameter set lives, so drift between the two
	// is a visible edit rather than a silent gap.
	check := func(typ string) func(map[string]any) error {
		return CheckSpawnParams(typ, SpawnParamsByType[typ])
	}
	types.register("reason", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildReasonSpec(ctx, cfg)
	}, check("reason"))
	types.register("coding", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildCodingSpec(ctx, cfg)
	}, check("coding"))
	types.register("workspace", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildWorkspaceSpec(ctx, cfg)
	}, check("workspace"))
	types.register("host", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildHostSpec(ctx, cfg)
	}, check("host"))
	types.register("timer", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildTimerSpec(ctx, cfg)
	}, check("timer"))
	types.register("niq", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildNIQSpec(ctx, cfg)
	}, check("niq"))
	types.register("program", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildProgramSpec(ctx, cfg)
	}, check("program"))
	types.register("history", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildHistorySpec(ctx, cfg)
	}, check("history"))
	types.register("directory", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildDirectorySpec(ctx, cfg)
	}, check("directory"))
	types.register("mcp", func(cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
		return buildMCPSpec(ctx, cfg)
	}, check("mcp"))
}

// aclFor resolves a worker's bus ACL: the declaration's explicit grant when it
// has one, otherwise the type's defaults.
func aclFor(cfg coreworker.WorkerConfig, defPub []event.PublishPattern, defSub []event.EventPattern) ([]event.PublishPattern, []event.EventPattern) {
	if len(cfg.PublishAllow) > 0 {
		defPub = cfg.PublishAllow
	}
	if len(cfg.SubscribeAllow) > 0 {
		defSub = cfg.SubscribeAllow
	}
	return defPub, defSub
}

// specConnect builds a Connect closure: registers the identity idempotently and
// creates a fresh, connected in-process worker-side channel. The identity's ACL
// comes from the declaration (cfg), falling back to the type's defaults.
func specConnect(ctx BuildContext, id, typ string, cfg coreworker.WorkerConfig, defPub []event.PublishPattern, defSub []event.EventPattern) func() (corebus.WorkerSideChannel, error) {
	pubAllow, subAllow := aclFor(cfg, defPub, defSub)
	return func() (corebus.WorkerSideChannel, error) {
		// The durable identity is the bus control plane's record. The type's
		// default grant seeds it on first registration; an edited grant already
		// on disk is never overwritten here.
		if err := ensureIdentity(ctx.Bus, corebus.Identity{
			WorkerID:       id,
			Type:           typ,
			PublishAllow:   pubAllow,
			SubscribeAllow: subAllow,
		}); err != nil {
			return nil, err
		}
		ch := inprocess.NewWorkerSide(id, ctx.Listener)
		if err := ch.Connect(context.Background(), "inproc://niq"); err != nil {
			return nil, err
		}
		return ch, nil
	}
}

// ── reason family: resolved inputs ──

// reasonFamilyIn is the computed construction input shared by every
// reason-family worker (the generic reason worker and the coding worker). It
// keeps the two builders on one code path: same param parsing, provider
// resolution, program seeding, budget knobs and durable-checkpoint hook.
type reasonFamilyIn struct {
	id               string
	pubAllow         []event.PublishPattern
	subAllow         []event.EventPattern
	programs         []programpkg.Program
	events           []reason.EventConverter
	providerSources  reasonbase.ProviderSources
	pname, pmodel    string
	budgetSoft       float64
	budgetHard       float64
	tailTokens       int
	compactDirective string
	readme           string
	seedBrief        []llm.Message
	// collaborators is the reason-worker collaboration whitelist; nil means
	// every reason peer is visible (see reason.Config.Collaborators).
	collaborators []string
}

// resolveReasonFamily computes the shared inputs from a worker config. typ is
// the worker type label used for validation messages and the bus identity.
func resolveReasonFamily(cfg coreworker.WorkerConfig, typ string) (*reasonFamilyIn, error) {
	id := cfg.ID
	if id == "" {
		return nil, fmt.Errorf("%s: id is required", typ)
	}
	p := cfg.Params

	// The provider is a NAME in provider.json; model is one of its models.
	// Endpoint and credentials live on that entry — a worker carries neither.
	provider, _ := p["provider"].(string)
	model, _ := p["model"].(string)
	if provider != "" {
		if _, ok := providerpkg.Find(provider); !ok {
			return nil, fmt.Errorf("%s: provider %q is not configured in provider.json", typ, provider)
		}
	}

	// SubscribeAllow: only broadcast delivery needs listing. Directed events
	// (tool calls, their request.* replies, timer.timeout, management
	// requests) reach the worker regardless; broadcast traffic to a reason
	// worker is just the worker presence lifecycle and worker.input (hiw
	// broadcasts when untargeted). The declaration's explicit ACL wins; this
	// is only the type default.
	//
	// PublishAllow default stays "*": a reason-family worker forwards tool
	// invocations to whichever peers it discovers, under the peers' own event
	// types — an inherently dynamic set that cannot be enumerated statically.
	pubAllow, subAllow := aclFor(cfg,
		[]event.PublishPattern{event.NewPublishPattern("*")},
		[]event.EventPattern{
			event.NewPattern("worker.ready"),
			event.NewPattern("worker.gone"),
			event.NewPattern("worker.discover"),
			event.NewPattern("worker.abort"),
			event.NewPattern("worker.input"),
		})
	programs := ParsePrograms(p, id)
	events := parseEvents(p)

	// Spawn seeding (context-builder.md §6): goal lands in the system prompt
	// (program space, survives compaction); brief lands as the transcript's
	// first message (working material, compactable).
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

	budgetSoft, _ := p["budget_soft"].(float64)
	budgetHard, _ := p["budget_hard"].(float64)
	// Tail retention is a token budget now, not a fixed message count. Accept
	// tail_tokens first with keep_tail as a legacy alias.
	tailTokens := pInt(p, "tail_tokens")
	if tailTokens == 0 {
		tailTokens = pInt(p, "keep_tail")
	}
	compactDirective, _ := p["compact_directive"].(string)
	readme, _ := p["readme"].(string)
	// collaborators: absent = unrestricted; present (even []) = whitelist.
	collaborators := pStrings(p, "collaborators")

	providerSources := providerpkg.NewProviderSources(provider, model)
	pname, pmodel := providerpkg.InitialProviderInfo(provider, model)

	return &reasonFamilyIn{
		id:               id,
		pubAllow:         pubAllow,
		subAllow:         subAllow,
		programs:         programs,
		events:           events,
		providerSources:  providerSources,
		pname:            pname,
		pmodel:           pmodel,
		budgetSoft:       budgetSoft,
		budgetHard:       budgetHard,
		tailTokens:       tailTokens,
		compactDirective: compactDirective,
		readme:           readme,
		seedBrief:        seedBrief,
		collaborators:    collaborators,
	}, nil
}

// ── reason worker ──

func buildReasonSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	in, err := resolveReasonFamily(cfg, "reason")
	if err != nil {
		return coreworker.SpawnSpec{}, err
	}
	connect := specConnect(ctx, in.id, "reason", cfg, in.pubAllow, in.subAllow)
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return reason.NewWorker(reason.Config{
			ID:               in.id,
			Provider:         in.providerSources.Default(),
			ProviderSources:  in.providerSources,
			ProviderName:     in.pname,
			ProviderModel:    in.pmodel,
			Programs:         in.programs,
			EventConverters:  in.events,
			Bus:              ch,
			BudgetSoft:       in.budgetSoft,
			BudgetHard:       in.budgetHard,
			TailTokens:       in.tailTokens,
			CompactDirective: in.compactDirective,
			Readme:           in.readme,
			Collaborators:    in.collaborators,
			// Fixed per-message payload cap for tool results / inputs.
			MaxPayloadBytes: 20 * 1024,
			SeedMessages:    in.seedBrief,
			// A runtime provider switch must outlive this process, so the
			// worker signals it and the assembly checkpoints it.
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(in.id); err != nil {
					log.Printf("[project] checkpoint %s: %v", in.id, err)
				}
			},
		})
	}
	cfg.Type = "reason"
	return coreworker.SpawnSpec{Config: cfg, Connect: connect, Build: build}, nil
}

// ── coding reason worker ──

func buildCodingSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	in, err := resolveReasonFamily(cfg, "coding")
	if err != nil {
		return coreworker.SpawnSpec{}, err
	}
	p := cfg.Params
	// tool_mode is the only coding-specific knob. PTC's program/directory peers
	// are part of the PTC contract, not configuration.
	toolMode, _ := p["tool_mode"].(string)

	connect := specConnect(ctx, in.id, "coding", cfg, in.pubAllow, in.subAllow)
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return coding.NewWorker(coding.Config{
			ID:               in.id,
			Provider:         in.providerSources.Default(),
			ProviderSources:  in.providerSources,
			ProviderName:     in.pname,
			ProviderModel:    in.pmodel,
			Programs:         in.programs,
			EventConverters:  in.events,
			Bus:              ch,
			BudgetSoft:       in.budgetSoft,
			BudgetHard:       in.budgetHard,
			TailTokens:       in.tailTokens,
			CompactDirective: in.compactDirective,
			Readme:           in.readme,
			Collaborators:    in.collaborators,
			MaxPayloadBytes:  20 * 1024,
			SeedMessages:     in.seedBrief,
			ToolMode:         toolMode,
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(in.id); err != nil {
					log.Printf("[project] checkpoint %s: %v", in.id, err)
				}
			},
		})
	}
	cfg.Type = "coding"
	return coreworker.SpawnSpec{Config: cfg, Connect: connect, Build: build}, nil
}

// ── program / event seeding ──

func programSeed(goal string) []programpkg.Program {
	return []programpkg.Program{{
		Meta: programpkg.Meta{
			Name:        "goal",
			ContentType: programpkg.ContentTypeInstruction,
		},
		EntryContent: programpkg.ProgramContent{Content: "# Goal\n\n" + goal},
	}}
}

// ParsePrograms extracts a simplified program list from spawn params. It is the
// inverse of the typed Programs field (see Params), exported because the
// template/preset tooling checks the same projection the builders consume.

// ── workspace ──

func buildWorkspaceSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	mounts, err := parseMountsParam(p)
	if err != nil {
		return coreworker.SpawnSpec{}, err
	}
	if len(mounts) == 0 {
		return coreworker.SpawnSpec{}, fmt.Errorf("workspace: at least one mount is required")
	}
	// Use the id passed in from the project/config definition. Only fall back to
	// a path-derived id when none was given (bare workspace).
	id := cfg.ID
	if id == "" {
		id = "ws-" + SanitizeID(mounts[0])
	}
	approver, _ := p["approver"].(string)
	if approver == "" {
		approver = DefaultWebUIID // default approver: the human UI worker
	}
	params := p
	params["mounts"] = mounts
	params["approver"] = approver
	cfg.ID = id
	cfg.Params = params

	// PublishAllow: the workspace replies to tool calls (request.*), asks its
	// approver to expand the boundary (approval.request), and announces
	// presence. SubscribeAllow: worker.discover, so the workspace re-announces
	// its tools to joiners. Tool calls arrive directed; the workspace consumes
	// no other broadcasts.
	connect := specConnect(ctx, id, "workspace", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
			event.NewPublishPattern("approval.request"),
		},
		[]event.EventPattern{event.NewPattern("worker.discover")})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		// The default mount ({project}/workspace) is seeded from a template
		// but never created on disk, so ensure each mount exists before the
		// backend uses it as the bash cwd — otherwise commands fail with a
		// confusing "fork/exec /bin/sh: no such file or directory" from the
		// chdir step.
		for _, m := range mounts {
			if err := os.MkdirAll(m, 0o755); err != nil {
				log.Printf("[project] workspace %s: create mount %q: %v", id, m, err)
			}
		}
		return workspace.New(workspace.Config{
			ID:       id,
			Bus:      ch,
			Backend:  wsbackend.NewEmbeddedBackend(mounts),
			Approver: approver,
			// A runtime mount.add must outlive this process, so the worker
			// signals it and the assembly layer checkpoints it — the same
			// arrangement as the reason worker's provider switch.
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(id); err != nil {
					log.Printf("[project] checkpoint %s: %v", id, err)
				}
			},
		})
	}
	cfg.Type = "workspace"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── host ──

func buildHostSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		id = DefaultHostWorkerID
	}
	// PublishAllow: lifecycle replies (request.*) and presence. Its tool
	// events (spawn/suspend/resume/restart/destroy) are directed calls — no
	// subscription needed; request.cancel is directed too. SubscribeAllow:
	// worker.discover, so the host re-announces its tools to any worker that
	// asks (late joiners, search refresh). The fleet roster lives on the
	// directory worker, not here — host manages lifecycles and must not be
	// misread as listing or suspending arbitrary workers.
	connect := specConnect(ctx, id, "host", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
			event.NewPublishPattern("worker.discover"),
		},
		[]event.EventPattern{event.NewPattern("worker.discover")})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return host.New(host.Config{ID: id, Bus: ch, Engine: ctx.WorkerBackend, Params: p})
	}
	cfg.ID = id
	cfg.Type = "host"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── timer ──

func buildTimerSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		id = "timer"
	}
	// PublishAllow: replies, the directed fires back to the caller, presence.
	// SubscribeAllow: worker.discover, so the timer re-announces its tools to
	// joiners. Everything else it receives is directed.
	connect := specConnect(ctx, id, "timer", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("timer.timeout"),
			event.NewPublishPattern("worker.input"),
			event.NewPublishPattern("worker.ready"),
		},
		[]event.EventPattern{event.NewPattern("worker.discover")})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return timer.New(timer.Config{ID: id, Bus: ch, Params: p,
			// A scheduled/cancelled timer must survive a restart, so the worker
			// signals it and the assembly layer checkpoints it — the same
			// arrangement as the reason worker's provider switch.
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(id); err != nil {
					log.Printf("[project] checkpoint %s: %v", id, err)
				}
			},
		})
	}
	cfg.ID = id
	cfg.Type = "timer"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── mcp ──

func buildMCPSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	id := cfg.ID
	if id == "" {
		id = "mcp"
	}
	mcf, err := mcp.ParseConfig(cfg.Params)
	if err != nil {
		return coreworker.SpawnSpec{}, err
	}
	connect := specConnect(ctx, id, "mcp", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
		},
		[]event.EventPattern{
			event.NewPattern("worker.discover"),
			event.NewPattern("worker.ready"),
			event.NewPattern("worker.gone"),
		})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return mcp.New(mcp.WorkerConfig{
			ID:           id,
			Bus:          ch,
			Expose:       mcf.Expose,
			Fixed:        mcf.Fixed,
			Servers:      mcf.Servers,
			AllowedTools: mcf.AllowedTools,
			Params:       cfg.Params,
			// A runtime server add/remove must outlive this process, so the
			// worker signals it and the assembly layer checkpoints it — the
			// same arrangement as the timer/workspace runtime changes.
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(id); err != nil {
					log.Printf("[project] checkpoint %s: %v", id, err)
				}
			},
		})
	}
	cfg.ID = id
	cfg.Type = "mcp"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── niq ──

// buildNIWSpec builds a NIW (niq interface worker) — a managed worker that, at
// Start, dials another niq instance's bus as an HTTP-transport remote worker.
// Its local identity is registered like any managed worker; the remote
// connection (remote URL / remote worker id / remote credential) comes from
// Params.
func buildNIQSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		return coreworker.SpawnSpec{}, fmt.Errorf("niq: id is required")
	}
	remoteURL, _ := p["remote_url"].(string)
	remoteWorkerID, _ := p["remote_worker_id"].(string)
	remoteCredRaw, _ := p["remote_credential"].(string)
	remoteCred := projprocess.ResolveSecret(remoteCredRaw)
	// recipient seeds the LOCAL binding, remote_recipient the REMOTE one. Both
	// are runtime properties (set via niq.recipient.*); the seeds just avoid a
	// bootstrap control event.
	recipient, _ := p["recipient"].(string)
	remoteRecipient, _ := p["remote_recipient"].(string)
	if remoteURL == "" || remoteWorkerID == "" {
		return coreworker.SpawnSpec{}, fmt.Errorf("niq %s: params remote_url and remote_worker_id are required", id)
	}

	// This worker is a bridge, so its grant is exactly what a bridge needs:
	// forward collaborative worker.input, answer request.* control replies, and
	// announce presence. No "*" — it must not be free to broadcast arbitrary
	// types on its own bus. Targets stay runtime (the recipient binding), so
	// grants are type-scoped, not target-scoped.
	connect := specConnect(ctx, id, niq.Type, cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("worker.input"),
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
		},
		[]event.EventPattern{
			event.NewPattern("worker.input"),
			event.NewPattern("worker.discover"),
		})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		// A re-bound recipient must survive a restart; the assembly layer
		// checkpoints the worker the same way it does any worker with state.
		backend := niq.New(niq.Config{
			ID:               id,
			Recipient:        recipient,
			RemoteRecipient:  remoteRecipient,
			RemoteURL:        remoteURL,
			RemoteWorkerID:   remoteWorkerID,
			RemoteCredential: remoteCred,
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(id); err != nil {
					log.Printf("[project] checkpoint %s: %v", id, err)
				}
			},
		})
		w := iface.New(iface.Config{
			ID:       id,
			Type:     niq.Type,
			Bus:      ch,
			Backends: []iface.Backend{backend},
		})
		// The builder is the only place that knows both halves, so it is where a
		// backend's tools get attached to the worker that serves them.
		backend.RegisterTools(w)
		return w
	}
	cfg.ID = id
	cfg.Type = niq.Type
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── program ──

func buildProgramSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		id = "program"
	}
	root := ctx.ProgramsRoot
	mounts, err := parseMountsParam(p)
	if err != nil {
		return coreworker.SpawnSpec{}, err
	}
	if len(mounts) > 0 {
		root = mounts[0]
	}
	if root == "" {
		root = filepath.Join(home.Root(), "programs")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return coreworker.SpawnSpec{}, fmt.Errorf("program: bad mount: %w", err)
	}
	os.MkdirAll(abs, 0755)

	// PublishAllow: replies and presence. SubscribeAllow: worker.discover, so
	// the program worker re-announces its tools to joiners. Program
	// query/update arrive directed.
	// PublishAllow includes "*": the script-program executor relays script-
	// initiated calls under THIS worker's identity, and those carry the target's
	// own event type (bash, ...) — inherently dynamic, so it cannot be
	// enumerated. The executor is the single controlled exit; a target
	// allow-list belongs there, not in the ACL.
	connect := specConnect(ctx, id, "program", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
			event.NewPublishPattern("*"),
		},
		// No roster is tracked here: target validation asks the directory worker
		// (see program.checkTarget), so only discovery is consumed.
		[]event.EventPattern{event.NewPattern("worker.discover")})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return programworker.New(programworker.Config{
			ID:  id,
			Bus: ch,
			// The whole program environment: a filesystem Store plus the in-process
			// QuickJS Executor. Either half is swappable (a remote gateway, an
			// object-store Store) without the worker changing.
			Backend: programpkg.Compose(pgbackend.New(abs), quickjsexec.New(quickjsexec.Options{})),
		})
	}
	cfg.ID = id
	cfg.Type = "program"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// programSeed builds the goal instruction program for a spawned reason
// worker. The goal lives in program space: rendered into the system prompt
// every round, never compacted away.

// ── history ──

func buildHistorySpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		id = "history"
	}
	// PublishAllow: replies (request.*) and presence (worker.ready). Its tool
	// events (history.list / history.get) are directed calls — no broadcast
	// subscription is needed to serve them. SubscribeAllow: worker.discover,
	// so the history worker re-announces its tools to joiners (late reason
	// workers, search refresh).
	connect := specConnect(ctx, id, "history", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
		},
		[]event.EventPattern{event.NewPattern("worker.discover")})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return history.New(history.Config{
			ID:               id,
			Bus:              ch,
			Store:            ctx.EventStore,
			MaxListEvents:    pInt(p, "max_list_events"),
			MaxListItemBytes: pInt(p, "max_list_item_bytes"),
			Params:           p,
		})
	}
	cfg.ID = id
	cfg.Type = "history"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// pStrings reads an optional string-list worker param. Accepts a JSON array or
// a comma-separated string. It returns nil when the key is absent ("not
// configured") and a non-nil slice when present, so callers can tell an empty
// whitelist from an unset one.
// ── directory ──

func buildDirectorySpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		id = "directory"
	}
	// The directory LEARNS the fleet from worker.ready/gone broadcasts and
	// serves search/describe as callable tools to peers. It
	// never touches lifecycle (no WorkerService): a neutral observer.
	// PublishAllow: replies + presence. SubscribeAllow: worker.discover (so it
	// re-announces and answers joiners) plus worker.ready/worker.gone to build
	// the roster.
	connect := specConnect(ctx, id, "directory", cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
			event.NewPublishPattern("worker.discover"),
		},
		[]event.EventPattern{
			event.NewPattern("worker.discover"),
			event.NewPattern("worker.ready"),
			event.NewPattern("worker.gone"),
		})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		return directory.New(directory.Config{ID: id, Bus: ch, Params: p})
	}
	cfg.ID = id
	cfg.Type = "directory"
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}
