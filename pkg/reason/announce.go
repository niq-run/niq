// The outbound announcement: how this worker tells the bus who it is and what
// it serves. This is the worker.ready side of discovery — the mirror of
// discovery.go, which is the inbound half that learns other workers'
// contracts.
//
// Note on naming: the announcement payload's field is literally "watch", so
// the rendering helpers below keep that word. They are unrelated to the
// inbound event loop (worker.go) that consumes events.
package reason

import (
	"context"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
)

// BroadcastReady re-announces this worker's presence (two-batch: peers then
// self). Exported for embedding workers and tests.
func (w *BaseReasonWorker) BroadcastReady() { w.broadcastReady() }

// broadcastReady announces this worker's presence in two ready events:
//
//   - a broadcast to every peer except this worker (ExcludeWorkerID) carrying
//     the externally callable contract, SelfOnly extensions left out, and
//   - a directed announcement to this worker carrying the full contract.
//
// Because the worker is excluded from its own broadcast, it receives exactly
// one self-sourced ready event, which is its complete view — so
// handleWorkerReady can treat each ready event as that worker's whole contract
// and replace it wholesale on re-announcement (no merge, no local seeding).
func (w *BaseReasonWorker) broadcastReady() {
	caps := w.Extensions()
	peerEntries := make([]map[string]any, 0, len(caps))
	for _, cap := range caps {
		if cap.SelfOnly {
			continue
		}
		peerEntries = append(peerEntries, w.extensionEntry(cap))
	}
	presence := event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"watch":     peerEntries,
	})
	presence.ExcludeWorkerID = w.ID()
	// First batch: presence to every peer except this worker. Carries the
	// externally callable contract only — SelfOnly extensions are left out, so
	// peers never see them as callable tools.
	_ = w.Channel.Broadcast(context.Background(), presence)

	// Second batch: the full contract (SelfOnly included) sent to itself. This
	// is the self-directed round-trip through discovery — handleWorkerReady
	// treats it like any peer's announcement, so this worker's own capabilities
	// flow through the same unified discovery pipeline as everyone else's.
	_ = w.Channel.Send(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "reason",
		"watch":     w.extensionEntries(),
	}), w.ID())
}

// extensionEntries renders every registered extension into the worker.ready
// "watch" wire format — the full contract, SelfOnly included. It is the
// self-directed announcement, from which the worker learns its complete own
// view. The peer-facing broadcast is built by broadcastReady, which filters
// SelfOnly extensions out before rendering; the renderer itself carries no
// policy.
func (w *BaseReasonWorker) extensionEntries() []map[string]any {
	caps := w.Extensions()
	out := make([]map[string]any, 0, len(caps))
	for _, cap := range caps {
		out = append(out, w.extensionEntry(cap))
	}
	return out
}

// extensionEntry renders one extension into a worker.ready "watch" entry.
// tool.request entries carry the tool name top-level; worker.update /
// worker.query entries fold the discriminator (op / subject) into parameters.
func (w *BaseReasonWorker) extensionEntry(cap baseworker.Extension) map[string]any {
	entry := map[string]any{
		"event": string(cap.Event),
		"desc":  cap.Description,
	}
	if cap.Event == event.TypeToolRequest {
		entry["name"] = cap.Key
		// Carry the schema too, so a peer tool's parameters reach both the
		// LLM tool list and the dispatch table.
		if len(cap.Parameters) > 0 {
			entry["parameters"] = cap.Parameters
		}
	} else if cap.KeyField != "" {
		params := cap.Parameters
		if params == nil {
			params = map[string]any{}
		}
		params = cloneParams(params)
		params[cap.KeyField] = cap.Key
		entry["parameters"] = params
	} else if len(cap.Parameters) > 0 {
		entry["parameters"] = cap.Parameters
	}
	return entry
}
