package workers

import (
	"log"

	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/core/impl/workers/reason"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── reason worker ──

func buildReasonSpec(ctx builtin.BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	in, err := resolveReasonFamily(ctx, cfg, "reason")
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

// ── spawn contract ──
//
// What this type accepts. It is registered with the builder above, so the
// contract and the implementation are one edit.
var spawnParamsReason = []string{
	"instruction", "goal", "brief", "programs", "events", "readme",
	"provider", "model", "collaborators",
	"budget_soft", "budget_hard",
	"tail_tokens", "keep_tail", "compact_directive",
}

func registerReason(ctx builtin.BuildContext, reg *builtin.Registry) {
	reg.RegisterType("reason", spawnParamsReason, ctx, buildReasonSpec)
}
