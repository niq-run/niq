package workers

import (
	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/core/impl/workers/directory"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── directory ──

func buildDirectorySpec(ctx builtin.BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
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

// ── spawn contract ──
var spawnParamsDirectory = []string{"readme", "readme_append"}

func registerDirectory(ctx builtin.BuildContext, reg *builtin.Registry) {
	reg.RegisterType("directory", spawnParamsDirectory, ctx, buildDirectorySpec)
}
