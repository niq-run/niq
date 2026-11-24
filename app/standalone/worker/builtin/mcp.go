package builtin

import (
	"log"

	"github.com/niq-run/niq/core/impl/workers/mcp"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

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

// ── spawn contract ──
var spawnParamsMCP = []string{"expose", "fixed", "servers", "allowed_tools", "mcp", "readme", "readme_append"}

func registerMCP(ctx BuildContext, reg *Registry) {
	reg.RegisterType("mcp", spawnParamsMCP, ctx, buildMCPSpec)
}
