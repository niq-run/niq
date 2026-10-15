package reason

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// ready helper: build a worker.ready announcement for a worker of the given
// type, carrying one watch capability.
func readyFor(workerID, typ, capEvt string) event.Event {
	return event.New(event.TypeWorkerReady, workerID, map[string]any{
		"worker_id": workerID,
		"type":      typ,
		"watch": []map[string]any{{
			"event": condStr(capEvt != "", capEvt, "noop.cap"),
			"desc":  "some capability",
		}},
	})
}

func condStr(ok bool, a, b string) string {
	if ok {
		return a
	}
	return b
}

// TestPeerReasonReadyIsNotATool verifies the core directory split: a reason
// worker's ready describes events it RESPONDS to, not callable tools for peers
// — so another reason worker must NOT surface a peer reason's extensions as
// tools. Infrastructure workers (here "workspace") still surface as tools.
func TestPeerReasonReadyIsNotATool(t *testing.T) {
	w, _ := newTestWorker2()

	// An infra worker's ready becomes a callable capability (workspace__bash).
	w.HandleWorkerReady(readyFor("workspace", "workspace", "bash"))
	if _, ok := w.capabilityByToolName("workspace__bash"); !ok {
		t.Fatalf("infra workspace__bash should be callable, got discovered=%+v", w.discovered)
	}

	// A peer reason worker's ready must NOT become a callable capability.
	w.HandleWorkerReady(readyFor("peer", "reason", "context.compress"))
	if _, ok := w.capabilityByToolName("peer__context_compress"); ok {
		t.Fatalf("peer reason's context_compress must NOT be callable, got discovered=%+v", w.discovered)
	}
	if len(w.discovered) != 1 {
		t.Fatalf("expected exactly the infra capability, got %d: %+v", len(w.discovered), w.discovered)
	}
}
