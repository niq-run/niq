package builtin

import (
	"github.com/niq-run/niq/core/impl/workers/host"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

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

// ── spawn contract ──
var spawnParamsHost = []string{"readme", "readme_append"}

func registerHost(ctx BuildContext, reg *Registry) {
	reg.RegisterType("host", spawnParamsHost, ctx, buildHostSpec)
}
