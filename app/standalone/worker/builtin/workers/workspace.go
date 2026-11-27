package workers

import (
	"fmt"
	"log"
	"os"

	"github.com/niq-run/niq/app/standalone/helper"
	"github.com/niq-run/niq/app/standalone/worker/builtin"
	wsbackend "github.com/niq-run/niq/core/impl/embedws"
	"github.com/niq-run/niq/core/impl/workers/workspace"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── workspace ──

func buildWorkspaceSpec(ctx builtin.BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
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
		id = "ws-" + helper.SafeName(mounts[0])
	}
	approver, _ := p["approver"].(string)
	if approver == "" {
		approver = builtin.DefaultWebUIID // default approver: the human UI worker
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

// ── spawn contract ──
var spawnParamsWorkspace = []builtin.ParamSpec{{Name: "mounts"}, {Name: "path"}, {Name: "approver"}}

func registerWorkspace(ctx builtin.BuildContext, reg *builtin.Registry) {
	reg.RegisterType("workspace", spawnParamsWorkspace, ctx, buildWorkspaceSpec)
}
