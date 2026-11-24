package workers

import (
	"context"

	"github.com/niq-run/niq/app/standalone/worker/builtin"
	"github.com/niq-run/niq/core/impl/eventbus/transport/inprocess"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// aclFor resolves a worker's bus ACL: the declaration's explicit grant when it
// has one, otherwise the type's defaults.
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
func specConnect(ctx builtin.BuildContext, id, typ string, cfg coreworker.WorkerConfig, defPub []event.PublishPattern, defSub []event.EventPattern) func() (corebus.WorkerSideChannel, error) {
	pubAllow, subAllow := aclFor(cfg, defPub, defSub)
	return func() (corebus.WorkerSideChannel, error) {
		// The durable identity is the bus control plane's record. The type's
		// default grant seeds it on first registration; an edited grant already
		// on disk is never overwritten here.
		if err := builtin.EnsureIdentity(ctx.Bus, corebus.Identity{
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
