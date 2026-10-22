package reason

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestReadmeSeedUpdateAndRestore verifies the self-description capability: the
// config seeds worker.readme, a directed update replaces it, and the value
// survives a snapshot/restore instead of falling back to the seed. Both halves
// are advertised (the update's sender is a bus ACL concern).
func TestReadmeSeedUpdateAndRestore(t *testing.T) {
	ch := newTestChannel()
	w := NewBaseReasonWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch, Readme: "seed text"})

	if got := w.Readme(); got != "seed text" {
		t.Fatalf("seed = %q, want the configured readme", got)
	}

	// Read half: worker.readme answers with the current value.
	read := event.New(event.TypeWorkerReadme, "peer", nil)
	read.RequestId = "req-read"
	if !w.DispatchExtension(read) {
		t.Fatal("worker.readme is not registered")
	}
	replies := ch.eventsOf(event.TypeRequestCompleted)
	if len(replies) != 1 {
		t.Fatalf("replies = %d, want 1", len(replies))
	}
	if got, _ := replies[0].Payload["result"].(string); got != "seed text" {
		t.Fatalf("read reply = %q, want the seed", got)
	}

	// Write half: a directed update (the worker itself or an authorized peer)
	// replaces the description; surrounding whitespace is trimmed.
	upd := event.New(TypeReadmeUpdate, "peer", map[string]any{"readme": "  new role  "})
	upd.RequestId = "req-upd"
	if !w.DispatchExtension(upd) {
		t.Fatal("worker.readme.update is not registered")
	}
	if got := w.Readme(); got != "new role" {
		t.Fatalf("updated readme = %q, want trimmed text", got)
	}

	// Both halves are advertised: the roster tells peers/HIW that the read and
	// write capabilities exist; the bus ACL decides who may send the update.
	if !hasWatch(w.WatchEntries(false), TypeReadmeUpdate) {
		t.Fatal("worker.readme.update must be announced to peers")
	}
	if !hasWatch(w.WatchEntries(true), TypeReadmeUpdate) {
		t.Fatal("worker.readme.update must be in the worker's own view")
	}
	if !hasWatch(w.WatchEntries(false), event.TypeWorkerReadme) {
		t.Fatal("worker.readme must be peer-announced")
	}

	// Durable: the snapshot carries the runtime value over the config seed.
	snap, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	w2 := NewBaseReasonWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: newTestChannel(), Readme: "seed text"})
	if err := w2.Restore(snap); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := w2.Readme(); got != "new role" {
		t.Fatalf("restored readme = %q, want the runtime value", got)
	}
}

// hasWatch reports whether a watch contract contains the event type.
func hasWatch(entries []map[string]any, typ event.EventType) bool {
	for _, e := range entries {
		if s, _ := e["event"].(string); s == string(typ) {
			return true
		}
	}
	return false
}
