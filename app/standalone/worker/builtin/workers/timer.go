package workers

import (
	"log"

	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/core/impl/workers/timer"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── timer ──

func buildTimerSpec(ctx builtin.BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
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

// ── spawn contract ──
var spawnParamsTimer = []string{"readme", "readme_append"}

func registerTimer(ctx builtin.BuildContext, reg *builtin.Registry) {
	reg.RegisterType("timer", spawnParamsTimer, ctx, buildTimerSpec)
}
