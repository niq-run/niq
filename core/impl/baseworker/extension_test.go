package baseworker

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestUnregisterRemovesExtensions verifies a worker can withdraw capabilities
// it registered dynamically; unknown events are a no-op.
func TestUnregisterRemovesExtensions(t *testing.T) {
	w := NewBaseWorker("w", nil)
	w.Register(Extension{Event: "a"}, func(event.Event) {})
	w.Register(Extension{Event: "b"}, func(event.Event) {})

	w.Unregister("a", "missing")

	var got []string
	for _, e := range w.Extensions() {
		got = append(got, string(e.Event))
	}
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("extensions after unregister = %v, want [b]", got)
	}
}
