package workers

import (
	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/core/impl/workers/history"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── history ──

func buildHistorySpec(ctx builtin.BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
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

// ── spawn contract ──
var spawnParamsHistory = []string{"max_list_events", "max_list_item_bytes", "readme", "readme_append"}

func registerHistory(ctx builtin.BuildContext, reg *builtin.Registry) {
	reg.RegisterType("history", spawnParamsHistory, ctx, buildHistorySpec)
}
