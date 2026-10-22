package eventbus

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

func eventsOfType(events []event.Event, typ event.EventType) []event.Event {
	var out []event.Event
	for _, e := range events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// TestDeniedSendRepliesRejected verifies a publish-denied request is answered:
// the sender receives a request.rejected carrying the original request id, so
// its pending call cannot hang on a silent drop.
func TestDeniedSendRepliesRejected(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	// caller may send tool.call only somewhere else, never to "tool".
	if err := registry.Register(corebus.Identity{
		WorkerID:     "caller",
		PublishAllow: []event.PublishPattern{{Type: "tool.call", Target: "someone-else"}},
	}); err != nil {
		t.Fatalf("register caller: %v", err)
	}
	if err := registry.Register(corebus.Identity{
		WorkerID:       "tool",
		SubscribeAllow: []event.EventPattern{event.NewPattern("*")},
	}); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	engine := NewEngine(registry, nil)
	callerCh := &recvChannel{id: "caller", worker: "caller"}
	toolCh := &recvChannel{id: "tool", worker: "tool"}
	for _, w := range []struct {
		id string
		ch corebus.BusSideChannel
	}{{"caller", callerCh}, {"tool", toolCh}} {
		if err := engine.Connect(w.id, w.ch); err != nil {
			t.Fatalf("connect %s: %v", w.id, err)
		}
	}

	evt := event.New("tool.call", "caller", nil)
	evt.RequestId = "req-1"
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{evt},
		Targets: []string{"tool"},
	}, "caller")

	if len(toolCh.got) != 0 {
		t.Fatalf("denied target received %d events, want 0", len(toolCh.got))
	}
	replies := eventsOfType(callerCh.got, event.TypeRequestRejected)
	if len(replies) != 1 {
		t.Fatalf("caller got %d request.rejected, want 1", len(replies))
	}
	reply := replies[0]
	if reply.RequestId != "req-1" {
		t.Fatalf("reply request id = %q, want req-1", reply.RequestId)
	}
	if reply.WorkerId != "tool" {
		t.Fatalf("reply attributed to %q, want the intended target tool", reply.WorkerId)
	}
	reason, _ := reply.Payload["reason"].(string)
	if !strings.Contains(reason, "publish denied") {
		t.Fatalf("reason = %q, want it to explain the denial", reason)
	}
}

// TestOfflineTargetRepliesFailed verifies a request to an unknown/offline worker
// is answered with request.failed instead of being dropped.
func TestOfflineTargetRepliesFailed(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(corebus.Identity{
		WorkerID:     "caller",
		PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")},
	}); err != nil {
		t.Fatalf("register caller: %v", err)
	}

	engine := NewEngine(registry, nil)
	callerCh := &recvChannel{id: "caller", worker: "caller"}
	if err := engine.Connect("caller", callerCh); err != nil {
		t.Fatalf("connect caller: %v", err)
	}

	evt := event.New("tool.call", "caller", nil)
	evt.RequestId = "req-2"
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{evt},
		Targets: []string{"gone"},
	}, "caller")

	replies := eventsOfType(callerCh.got, event.TypeRequestFailed)
	if len(replies) != 1 {
		t.Fatalf("caller got %d request.failed, want 1", len(replies))
	}
	reply := replies[0]
	if reply.RequestId != "req-2" {
		t.Fatalf("reply request id = %q, want req-2", reply.RequestId)
	}
	errText, _ := reply.Payload["error"].(string)
	if !strings.Contains(errText, "not online") {
		t.Fatalf("error = %q, want it to name the offline target", errText)
	}
}

// TestNotificationDenialDoesNotReply verifies only requests get a synthetic
// reply: a notification (no request id) has nothing waiting on it.
func TestNotificationDenialDoesNotReply(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := registry.Register(corebus.Identity{
		WorkerID:     "caller",
		PublishAllow: []event.PublishPattern{{Type: "tool.call", Target: "someone-else"}},
	}); err != nil {
		t.Fatalf("register caller: %v", err)
	}
	if err := registry.Register(corebus.Identity{
		WorkerID:       "tool",
		SubscribeAllow: []event.EventPattern{event.NewPattern("*")},
	}); err != nil {
		t.Fatalf("register tool: %v", err)
	}

	engine := NewEngine(registry, nil)
	callerCh := &recvChannel{id: "caller", worker: "caller"}
	toolCh := &recvChannel{id: "tool", worker: "tool"}
	for _, w := range []struct {
		id string
		ch corebus.BusSideChannel
	}{{"caller", callerCh}, {"tool", toolCh}} {
		if err := engine.Connect(w.id, w.ch); err != nil {
			t.Fatalf("connect %s: %v", w.id, err)
		}
	}

	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{event.New("tool.call", "caller", nil)},
		Targets: []string{"tool"},
	}, "caller")

	if len(callerCh.got) != 0 {
		t.Fatalf("caller got %d events for a denied notification, want 0", len(callerCh.got))
	}
}
