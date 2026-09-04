package eventbus

import (
	"context"
	"path/filepath"
	"testing"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// recvChannel is a minimal BusSideChannel that records whatever the engine
// delivers to it.
type recvChannel struct {
	id     string
	worker string
	got    []event.Event
}

func (m *recvChannel) ID() string       { return m.id }
func (m *recvChannel) WorkerID() string { return m.worker }
func (m *recvChannel) Send(_ context.Context, evt event.Event) error {
	m.got = append(m.got, evt)
	return nil
}
func (m *recvChannel) Receive(_ context.Context) (<-chan corebus.Request, error) {
	return make(chan corebus.Request), nil
}
func (m *recvChannel) Close() error { return nil }

// TestBroadcastExcludesWorker verifies ExcludeWorkerID: a worker excluded from
// a broadcast does not receive it even though it matches the subscription,
// while every other matching worker does.
func TestBroadcastExcludesWorker(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := registry.Register(corebus.Identity{
			WorkerID:       id,
			PublishAllow:   []event.PublishPattern{event.NewPublishPattern("*")},
			SubscribeAllow: []event.EventPattern{event.NewPattern("worker.ready")},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	engine := NewEngine(registry, nil)
	ca := &recvChannel{id: "a", worker: "a"}
	cb := &recvChannel{id: "b", worker: "b"}
	cc := &recvChannel{id: "c", worker: "c"}
	for _, w := range []struct {
		id string
		ch corebus.BusSideChannel
	}{{"a", ca}, {"b", cb}, {"c", cc}} {
		if err := engine.Connect(w.id, w.ch); err != nil {
			t.Fatalf("connect %s: %v", w.id, err)
		}
	}

	evt := event.New(event.TypeWorkerReady, "a", map[string]any{"worker_id": "a"})
	evt.ExcludeWorkerID = "a"
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:   corebus.RequestBroadcast,
		Events: []event.Event{evt},
	}, "a")

	if len(ca.got) != 0 {
		t.Fatalf("excluded worker a received %d events", len(ca.got))
	}
	if len(cb.got) != 1 || len(cc.got) != 1 {
		t.Fatalf("workers b/c should each receive 1 event, got b=%d c=%d", len(cb.got), len(cc.got))
	}
}

// TestDisconnectBroadcastsGone verifies a worker leaving the bus (delete,
// suspend, crash) broadcasts worker.gone naming it, so peers (reason discovery)
// can drop its tools/events.
func TestDisconnectBroadcastsGone(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if err := registry.Register(corebus.Identity{
			WorkerID:       id,
			SubscribeAllow: []event.EventPattern{event.NewPattern("worker.*")},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	engine := NewEngine(registry, nil)
	ca := &recvChannel{id: "a", worker: "a"}
	cb := &recvChannel{id: "b", worker: "b"}
	if err := engine.Connect("a", ca); err != nil {
		t.Fatalf("connect a: %v", err)
	}
	if err := engine.Connect("b", cb); err != nil {
		t.Fatalf("connect b: %v", err)
	}

	engine.Disconnect("a")

	if engine.Channel("a") != nil {
		t.Fatalf("a still connected after Disconnect")
	}
	if len(cb.got) != 1 || cb.got[0].Type != event.TypeWorkerGone {
		t.Fatalf("b was not told a is gone; got %+v", cb.got)
	}
	if got, _ := cb.got[0].Payload["worker_id"].(string); got != "a" {
		t.Fatalf("worker.gone worker_id = %q, want a", got)
	}
}
