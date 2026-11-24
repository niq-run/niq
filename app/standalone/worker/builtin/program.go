package builtin

import (
	"fmt"
	"os"
	"path/filepath"

	programworker "github.com/niq-run/niq/core/impl/workers/program"
	"github.com/niq-run/niq/core/impl/workers/program/pgbackend"
	"github.com/niq-run/niq/core/impl/workers/program/quickjsexec"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	programpkg "github.com/niq-run/niq/core/itfs/program"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

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
		return coreworker.SpawnSpec{}, fmt.Errorf("program: programs root is not configured")
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

// ── spawn contract ──
var spawnParamsProgram = []string{"mounts", "path"}

func registerProgram(ctx BuildContext, reg *Registry) {
	reg.RegisterType("program", spawnParamsProgram, ctx, buildProgramSpec)
}
