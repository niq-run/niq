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

// TestWatchEntriesCarriesResponse verifies the declared reply shape rides the
// advertised contract (it is how peers, the directory roster and PTC scripts
// learn what a capability returns).
func TestWatchEntriesCarriesResponse(t *testing.T) {
	w := NewBaseWorker("w1", nil)
	w.Register(Extension{
		Event:       "typed",
		Description: "returns a typed reply",
		Parameters:  map[string]any{"type": "object"},
		Response:    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, func(event.Event) {})
	w.Register(Extension{Event: "plain", Description: "no schema"}, func(event.Event) {})

	entries := w.WatchEntries(false)
	byEvent := map[string]map[string]any{}
	for _, e := range entries {
		ev, _ := e["event"].(string)
		byEvent[ev] = e
	}
	got, ok := byEvent["typed"]["response"].(map[string]any)
	if !ok || got["type"] != "array" {
		t.Fatalf("typed.response = %#v, want the declared schema", byEvent["typed"]["response"])
	}
	if _, present := byEvent["plain"]["response"]; present {
		t.Fatal("a capability without a Response must not advertise one")
	}
}
