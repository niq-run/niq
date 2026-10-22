package baseworker

import (
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestInternalExtensionRefusesForeignSender verifies an Internal extension is
// self-scoped: a peer's request never reaches the handler and is rejected back,
// while the owner invokes it normally.
func TestInternalExtensionRefusesForeignSender(t *testing.T) {
	ch := &recordingChannel{}
	w := NewBaseWorker("w1", ch)
	ran := 0
	w.Register(Extension{Event: "internal.op", Internal: true}, func(event.Event) { ran++ })

	peer := event.New("internal.op", "peer", nil)
	peer.RequestId = "r1"
	if !w.DispatchExtension(peer) {
		t.Fatal("a refusal should count as handled")
	}
	if ran != 0 {
		t.Fatal("internal handler ran for a foreign sender")
	}
	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeRequestRejected || ch.sent[0].RequestId != "r1" {
		t.Fatalf("expected a request.rejected for r1, got %+v", ch.sent)
	}

	// A notification (no request id) is refused silently.
	note := event.New("internal.op", "peer", nil)
	if !w.DispatchExtension(note) {
		t.Fatal("a refusal should count as handled")
	}
	if len(ch.sent) != 1 {
		t.Fatalf("a notification refusal must not reply, got %d sends", len(ch.sent))
	}

	// The owner invokes it normally.
	mine := event.New("internal.op", "w1", nil)
	if !w.DispatchExtension(mine) || ran != 1 {
		t.Fatal("internal handler did not run for its owner")
	}
}

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
