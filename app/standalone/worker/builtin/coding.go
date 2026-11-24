package builtin

import (
	"log"

	"github.com/niq-run/niq/core/impl/workers/coding"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── coding reason worker ──

func buildCodingSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	in, err := resolveReasonFamily(ctx, cfg, "coding")
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

// ── spawn contract ──
var spawnParamsCoding = []string{
	"instruction", "goal", "brief", "programs", "events", "readme",
	"provider", "model", "collaborators",
	"budget_soft", "budget_hard",
	"tail_tokens", "keep_tail", "compact_directive",
	"tool_mode",
}

func registerCoding(ctx BuildContext, reg *Registry) {
	reg.RegisterType("coding", spawnParamsCoding, ctx, buildCodingSpec)
}
