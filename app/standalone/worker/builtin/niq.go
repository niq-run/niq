package builtin

import (
	"fmt"
	"log"

	projprocess "github.com/niq-run/niq/app/standalone/worker/process"
	"github.com/niq-run/niq/core/impl/iface"
	"github.com/niq-run/niq/core/impl/workers/niq"
	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
	coreworker "github.com/niq-run/niq/core/itfs/worker"
)

// ── niq ──

// buildNIWSpec builds a NIW (niq interface worker) — a managed worker that, at
// Start, dials another niq instance's bus as an HTTP-transport remote worker.
// Its local identity is registered like any managed worker; the remote
// connection (remote URL / remote worker id / remote credential) comes from
// Params.
func buildNIQSpec(ctx BuildContext, cfg coreworker.WorkerConfig) (coreworker.SpawnSpec, error) {
	p := cfg.Params
	id := cfg.ID
	if id == "" {
		return coreworker.SpawnSpec{}, fmt.Errorf("niq: id is required")
	}
	remoteURL, _ := p["remote_url"].(string)
	remoteWorkerID, _ := p["remote_worker_id"].(string)
	remoteCredRaw, _ := p["remote_credential"].(string)
	remoteCred := projprocess.ResolveSecret(remoteCredRaw)
	// recipient seeds the LOCAL binding, remote_recipient the REMOTE one. Both
	// are runtime properties (set via niq.recipient.*); the seeds just avoid a
	// bootstrap control event.
	recipient, _ := p["recipient"].(string)
	remoteRecipient, _ := p["remote_recipient"].(string)
	if remoteURL == "" || remoteWorkerID == "" {
		return coreworker.SpawnSpec{}, fmt.Errorf("niq %s: params remote_url and remote_worker_id are required", id)
	}

	// This worker is a bridge, so its grant is exactly what a bridge needs:
	// forward collaborative worker.input, answer request.* control replies, and
	// announce presence. No "*" — it must not be free to broadcast arbitrary
	// types on its own bus. Targets stay runtime (the recipient binding), so
	// grants are type-scoped, not target-scoped.
	connect := specConnect(ctx, id, niq.Type, cfg,
		[]event.PublishPattern{
			event.NewPublishPattern("worker.input"),
			event.NewPublishPattern("request.*"),
			event.NewPublishPattern("worker.ready"),
		},
		[]event.EventPattern{
			event.NewPattern("worker.input"),
			event.NewPattern("worker.discover"),
		})
	build := func(ch corebus.WorkerSideChannel) coreworker.ManagedWorker {
		// A re-bound recipient must survive a restart; the assembly layer
		// checkpoints the worker the same way it does any worker with state.
		backend := niq.New(niq.Config{
			ID:               id,
			Recipient:        recipient,
			RemoteRecipient:  remoteRecipient,
			RemoteURL:        remoteURL,
			RemoteWorkerID:   remoteWorkerID,
			RemoteCredential: remoteCred,
			OnDurableChange: func() {
				if err := ctx.Manager.Checkpoint(id); err != nil {
					log.Printf("[project] checkpoint %s: %v", id, err)
				}
			},
		})
		w := iface.New(iface.Config{
			ID:       id,
			Type:     niq.Type,
			Bus:      ch,
			Backends: []iface.Backend{backend},
		})
		// The builder is the only place that knows both halves, so it is where a
		// backend's tools get attached to the worker that serves them.
		backend.RegisterTools(w)
		return w
	}
	cfg.ID = id
	cfg.Type = niq.Type
	return coreworker.SpawnSpec{
		Config:  cfg,
		Connect: connect,
		Build:   build,
	}, nil
}

// ── spawn contract ──
var spawnParamsNiq = []string{"remote_url", "remote_worker_id", "remote_credential", "recipient", "remote_recipient"}

func registerNiq(ctx BuildContext, reg *Registry) {
	reg.RegisterType("niq", spawnParamsNiq, ctx, buildNIQSpec)
}
