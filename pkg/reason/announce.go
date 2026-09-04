// The outbound announcement: how this worker tells the bus who it is and what
// it serves. This is the worker.ready side of discovery — the mirror of
// discovery.go, which is the inbound half that learns other workers'
// contracts.
//
// The generic broadcast is baseworker.AnnounceReady (every worker's ready
// announcement); reason adds one extra half: the directed full contract sent
// to itself, so it can learn its own capabilities through the same discovery
// pipeline as everyone else's.
//
// Note on naming: the announcement payload's field is literally "watch", so
// the rendering helpers keep that word. They are unrelated to the inbound
// event loop (worker.go) that consumes events.
package reason

import (
	"context"

	"github.com/niq-run/niq/core/event"
)

// BroadcastReady re-announces this worker's presence (two-batch: peers then
// self). Exported for embedding workers and tests.

// BroadcastReady announces this worker's presence in two ready events:
//
//   - a broadcast to every peer except this worker (ExcludeWorkerID, via
//     baseworker.AnnounceReady) carrying the externally callable contract,
//     SelfOnly extensions left out, and
//   - a directed announcement to this worker carrying the full contract.
//
// Because the worker is excluded from its own broadcast, it receives exactly
// one self-sourced ready event, which is its complete view — so
// HandleWorkerReady can treat each ready event as that worker's whole contract
// and replace it wholesale on re-announcement (no merge, no local seeding).
func (w *BaseReasonWorker) BroadcastReady() {
	// First batch: presence to every peer except this worker. Carries the
	// externally callable contract only — SelfOnly extensions are left out, so
	// peers never see them as callable tools. This is the generic announce
	// every worker uses; only the self-directed half below is reason-specific.
	w.AnnounceReady("reason", nil)

	// Second batch: the full contract (SelfOnly included) sent to itself. This
	// is the self-directed round-trip through discovery — HandleWorkerReady
	// treats it like any peer's announcement, so this worker's own capabilities
	// flow through the same unified discovery pipeline as everyone else's.
	_ = w.Channel.Send(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"type":  "reason",
		"watch": w.ExtensionEntries(),
	}), w.ID())
}

// ExtensionEntries renders every registered extension into the worker.ready
// "watch" wire format — the full contract, SelfOnly included. It is the
// self-directed announcement, from which the worker learns its complete own
// view. The peer-facing broadcast (baseworker.AnnounceReady) filters SelfOnly
// extensions out before rendering; the renderer itself carries no policy.
func (w *BaseReasonWorker) ExtensionEntries() []map[string]any {
	return w.WatchEntries(true)
}
