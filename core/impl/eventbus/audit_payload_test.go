package eventbus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// mutatingChannel stands in for a worker handler: it decorates the payload it
// receives, which is what workers are allowed to do (baseworker exposes the
// event's payload as the tool arguments).
type mutatingChannel struct {
	id     string
	worker string
	got    []event.Event
}

func (m *mutatingChannel) ID() string       { return m.id }
func (m *mutatingChannel) WorkerID() string { return m.worker }
func (m *mutatingChannel) Send(_ context.Context, evt event.Event) error {
	evt.Payload["decorated"] = true
	m.got = append(m.got, evt)
	return nil
}
func (m *mutatingChannel) Receive(_ context.Context) (<-chan corebus.Request, error) {
	return make(chan corebus.Request), nil
}
func (m *mutatingChannel) Close() error { return nil }

// TestHandlerMutationDoesNotReachTheAuditLog pins the snapshot rule: the log
// keeps the event as it was routed, not as a handler decorated it.
//
// This is not only about faithful history. The log encodes the payload, and a
// handler writing into the same map while json.Marshal walks it is a runtime
// fatal — "concurrent map iteration and map write" — which no recover can turn
// into an error. A project died exactly this way under repeated worker
// creation, which is how the sharing was found.
func TestHandlerMutationDoesNotReachTheAuditLog(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, id := range []struct{ worker, typ string }{{"sender", "peer"}, {"target", "peer"}} {
		if err := registry.Register(corebus.Identity{
			WorkerID:       id.worker,
			Type:           id.typ,
			PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
			SubscribeAllow: []event.EventPattern{event.NewPattern("*")},
		}); err != nil {
			t.Fatalf("register %s: %v", id.worker, err)
		}
	}

	store := &recStore{}
	engine := NewEngine(registry, store)
	target := &mutatingChannel{id: "ch-target", worker: "target"}
	if err := engine.Connect("target", target); err != nil {
		t.Fatalf("connect target: %v", err)
	}

	evt := event.New("e2e.type", "sender", map[string]any{"k": "v"})
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Targets: []string{"target"},
		Events:  []event.Event{evt},
	}, "sender")

	// The handler saw (and decorated) its copy…
	if len(target.got) != 1 {
		t.Fatalf("delivered %d events, want 1", len(target.got))
	}
	if target.got[0].Payload["decorated"] != true {
		t.Fatalf("the handler's copy was not the one it decorated: %+v", target.got[0].Payload)
	}

	// …and the log kept the routed event.
	if len(store.evts) != 1 {
		t.Fatalf("persisted %d events, want 1", len(store.evts))
	}
	if _, ok := store.evts[0].Payload["decorated"]; ok {
		t.Fatalf("the audit log has the handler's decoration: %+v", store.evts[0].Payload)
	}
	if store.evts[0].Payload["k"] != "v" {
		t.Fatalf("the audit log lost the routed payload: %+v", store.evts[0].Payload)
	}

	// The stored event must still be encodable — this is what used to run
	// concurrently with the handler.
	if _, err := json.Marshal(store.evts[0]); err != nil {
		t.Fatalf("the persisted event does not encode: %v", err)
	}
}
