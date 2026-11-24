package builtin

import (
	"context"

	providerpkg "github.com/niq-run/niq/app/standalone/provider"
	"github.com/niq-run/niq/core/impl/eventbus"
	eventbusapi "github.com/niq-run/niq/core/impl/eventbus/api"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	"github.com/niq-run/niq/core/impl/workerctl"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

type BuildContext struct {
	Registry   corebus.IdentityRegistry
	Bus        corebus.Control
	Listener   *inprocess.InProcListener
	Engine     *eventbus.Engine
	Manager    *workerctl.Manager
	EventLog   *eventbusapi.EventLog
	EventStore event.EventStore
	// ProviderLoad reads provider.json. It is injected (store.LoadProvider in
	// production) so builders never resolve the config path or read the file.
	ProviderLoad providerpkg.Loader
	ProgramsRoot string
	// StateDir is the per-project workers state directory (`<proj>/workers`);
	// built-in workers persist their own state here (one dir per id).
	StateDir string
	// WorkerBackend is the control plane the host worker drives: the same
	// instance the assembly uses for recovery and shutdown.
	WorkerBackend coreworker.Backend
}

// registerBuilders registers one builder per known worker type, each with its
// spawn-parameter check.

func aclFor(cfg coreworker.WorkerConfig, defPub []event.PublishPattern, defSub []event.EventPattern) ([]event.PublishPattern, []event.EventPattern) {
	if len(cfg.PublishAllow) > 0 {
		defPub = cfg.PublishAllow
	}
	if len(cfg.SubscribeAllow) > 0 {
		defSub = cfg.SubscribeAllow
	}
	return defPub, defSub
}

// specConnect builds a Connect closure: registers the identity idempotently and
// creates a fresh, connected in-process worker-side channel. The identity's ACL
// comes from the declaration (cfg), falling back to the type's defaults.
func specConnect(ctx BuildContext, id, typ string, cfg coreworker.WorkerConfig, defPub []event.PublishPattern, defSub []event.EventPattern) func() (corebus.WorkerSideChannel, error) {
	pubAllow, subAllow := aclFor(cfg, defPub, defSub)
	return func() (corebus.WorkerSideChannel, error) {
		// The durable identity is the bus control plane's record. The type's
		// default grant seeds it on first registration; an edited grant already
		// on disk is never overwritten here.
		if err := EnsureIdentity(ctx.Bus, corebus.Identity{
			WorkerID:       id,
			Type:           typ,
			PublishAllow:   pubAllow,
			SubscribeAllow: subAllow,
		}); err != nil {
			return nil, err
		}
		ch := inprocess.NewWorkerSide(id, ctx.Listener)
		if err := ch.Connect(context.Background(), "inproc://niq"); err != nil {
			return nil, err
		}
		return ch, nil
	}
}
