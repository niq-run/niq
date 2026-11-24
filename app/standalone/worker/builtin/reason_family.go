package builtin

import (
	"fmt"

	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	reasonbase "github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/impl/workers/reason"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
	programpkg "github.com/niq-run/niq/core/itfs/program"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

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
func resolveReasonFamily(ctx BuildContext, cfg coreworker.WorkerConfig, typ string) (*reasonFamilyIn, error) {
	id := cfg.ID
	if id == "" {
		return nil, fmt.Errorf("%s: id is required", typ)
	}
	if ctx.ProviderLoad == nil {
		return nil, fmt.Errorf("%s: provider config is not wired", typ)
	}
	providerCfg, err := ctx.ProviderLoad()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", typ, err)
	}
	p := cfg.Params

	// The provider is a NAME in provider.json; model is one of its models.
	// Endpoint and credentials live on that entry — a worker carries neither.
	provider, _ := p["provider"].(string)
	model, _ := p["model"].(string)
	if provider != "" {
		if _, ok := providerpkg.Find(providerCfg, provider); !ok {
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

	providerSources := providerpkg.NewProviderSources(ctx.ProviderLoad, provider, model)
	pname, pmodel := providerpkg.InitialProviderInfo(providerCfg, provider, model)

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
