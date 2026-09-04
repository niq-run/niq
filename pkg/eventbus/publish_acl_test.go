package eventbus

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
)

// TestPublishTargetACL verifies the publisher-side ACL introduced alongside the
// target restriction:
//   - a worker can directed-send only to targets its PublishAllow names
//   - a worker with only a targeted grant cannot broadcast that type
//   - an all-type grant still allows both send and broadcast
func TestPublishTargetACL(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	// pub: may send request.completed only to "tool-a"; may not broadcast it.
	if err := registry.Register(corebus.Identity{
		WorkerID:     "pub",
		PublishAllow: []event.PublishPattern{{Type: "request.completed", Target: "tool-a"}},
	}); err != nil {
		t.Fatalf("register pub: %v", err)
	}
	// star: unrestricted.
	if err := registry.Register(corebus.Identity{
		WorkerID:     "star",
		PublishAllow: []event.PublishPattern{event.NewPublishPattern("*")},
	}); err != nil {
		t.Fatalf("register star: %v", err)
	}
	// Receivers.
	for _, id := range []string{"tool-a", "tool-b"} {
		if err := registry.Register(corebus.Identity{
			WorkerID:       id,
			SubscribeAllow: []event.EventPattern{event.NewPattern("*")},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	engine := NewEngine(registry, nil)
	cta := &recvChannel{id: "tool-a", worker: "tool-a"}
	ctb := &recvChannel{id: "tool-b", worker: "tool-b"}
	for _, w := range []struct {
		id string
		ch corebus.BusSideChannel
	}{{"tool-a", cta}, {"tool-b", ctb}} {
		if err := engine.Connect(w.id, w.ch); err != nil {
			t.Fatalf("connect %s: %v", w.id, err)
		}
	}

	evt := event.New("request.completed", "pub", nil)

	// Allowed target receives the directed send.
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{evt},
		Targets: []string{"tool-a"},
	}, "pub")
	if len(cta.got) != 1 || cta.got[0].Type != "request.completed" {
		t.Fatalf("tool-a got %d events, want 1", len(cta.got))
	}

	// Disallowed target is skipped.
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{evt},
		Targets: []string{"tool-b"},
	}, "pub")
	if len(ctb.got) != 0 {
		t.Fatalf("tool-b got %d events, want 0 (denied)", len(ctb.got))
	}

	// A broadcast of the targeted type is denied for pub.
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:   corebus.RequestBroadcast,
		Events: []event.Event{evt},
	}, "pub")
	if len(cta.got) != 1 {
		t.Fatalf("broadcast leaked to tool-a, want unchanged 1, got %d", len(cta.got))
	}

	// The unrestricted worker can both send anywhere and broadcast.
	starEvt := event.New("request.completed", "star", nil)
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:    corebus.RequestSend,
		Events:  []event.Event{starEvt},
		Targets: []string{"tool-a", "tool-b"},
	}, "star")
	if len(cta.got) != 2 || len(ctb.got) != 1 {
		t.Fatalf("star send: tool-a=%d (want 2), tool-b=%d (want 1)", len(cta.got), len(ctb.got))
	}
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:   corebus.RequestBroadcast,
		Events: []event.Event{starEvt},
	}, "star")
	if len(cta.got) != 3 || len(ctb.got) != 2 {
		t.Fatalf("star broadcast: tool-a=%d (want 3), tool-b=%d (want 2)", len(cta.got), len(ctb.got))
	}
}

// TestPublishPatternBackwardCompat verifies a bare-string PublishAllow grants
// both broadcast and any-target send, and that JSON unmarshalling of the old
// string-only form still works.
func TestPublishPatternBackwardCompat(t *testing.T) {
	if !event.NewPublishPattern("request.completed").BroadcastAllowed("request.completed") {
		t.Fatal("NewPublishPattern should allow broadcast")
	}
	if !event.NewPublishPattern("request.completed").SendAllowed("request.completed", "anywhere") {
		t.Fatal("NewPublishPattern should allow send to any target")
	}

	tgt := event.PublishPattern{Type: "request.completed", Target: "tool-a"}
	if tgt.BroadcastAllowed("request.completed") {
		t.Fatal("targeted grant must not allow broadcast")
	}
	if !tgt.SendAllowed("request.completed", "tool-a") {
		t.Fatal("targeted grant should allow send to its target")
	}
	if tgt.SendAllowed("request.completed", "tool-b") {
		t.Fatal("targeted grant must not allow send to another target")
	}

	// Type mismatch.
	if event.NewPublishPattern("request.completed").SendAllowed("worker.ready", "tool-a") {
		t.Fatal("type mismatch must be denied")
	}

	// Explicit "*" target is unrestricted (any target + broadcast).
	star := event.PublishPattern{Type: "request.completed", Target: "*"}
	if !star.BroadcastAllowed("request.completed") {
		t.Fatal("explicit * target should allow broadcast")
	}
	if !star.SendAllowed("request.completed", "somewhere") {
		t.Fatal("explicit * target should allow send to any target")
	}

	// An empty target matches nothing (no legacy alias): both send and broadcast
	// are denied.
	none := event.PublishPattern{Type: "request.completed"}
	if none.BroadcastAllowed("request.completed") {
		t.Fatal("empty target must not allow broadcast")
	}
	if none.SendAllowed("request.completed", "somewhere") {
		t.Fatal("empty target must not allow send")
	}

	// Old string-only registry form still parses.
	var pp event.PublishPattern
	if err := json.Unmarshal([]byte(`"*"`), &pp); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if pp.Type != "*" || pp.Target != "*" {
		t.Fatalf("unmarshal string -> %+v, want {type:* target:*}", pp)
	}
}

// TestPublishTypeWildcardTargetNarrowing verifies the narrowing path for a
// worker that starts at {type: "*", target: "*"} and is tightened to
// {type: "*", target: X} grants: any event type may still be sent, but only
// to the named targets, broadcasting is denied, and multiple targeted grants
// union. This is the reason-worker shape — its tool invocations use whatever
// event types the peers announced, so the type dimension stays "*" while the
// target dimension narrows.
func TestPublishTypeWildcardTargetNarrowing(t *testing.T) {
	registry, err := NewFileIdentityRegistry(filepath.Join(t.TempDir(), "identities.json"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	// Narrowed grants: any event type, but only to niq / timer.
	if err := registry.Register(corebus.Identity{
		WorkerID: "orchestrator",
		PublishAllow: []event.PublishPattern{
			{Type: "*", Target: "niq"},
			{Type: "*", Target: "timer"},
		},
	}); err != nil {
		t.Fatalf("register orchestrator: %v", err)
	}
	for _, id := range []string{"niq", "timer", "workspace"} {
		if err := registry.Register(corebus.Identity{
			WorkerID:       id,
			SubscribeAllow: []event.EventPattern{event.NewPattern("*")},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	engine := NewEngine(registry, nil)
	chans := map[string]*recvChannel{}
	for _, id := range []string{"niq", "timer", "workspace"} {
		c := &recvChannel{id: id, worker: id}
		chans[id] = c
		if err := engine.Connect(id, c); err != nil {
			t.Fatalf("connect %s: %v", id, err)
		}
	}

	send := func(typ event.EventType, target string) {
		engine.HandleRequest(context.Background(), corebus.Request{
			Type:    corebus.RequestSend,
			Events:  []event.Event{event.New(typ, "orchestrator", nil)},
			Targets: []string{target},
		}, "orchestrator")
	}

	// Any type reaches the named targets.
	send("ls", "niq")
	send("worker.input", "timer")
	if len(chans["niq"].got) != 1 || len(chans["timer"].got) != 1 {
		t.Fatalf("named targets: niq=%d timer=%d, want 1/1", len(chans["niq"].got), len(chans["timer"].got))
	}

	// An unnamed target is denied even though the type is "*".
	send("ls", "workspace")
	if len(chans["workspace"].got) != 0 {
		t.Fatalf("workspace got %d events, want 0 (denied)", len(chans["workspace"].got))
	}

	// A broadcast is denied: targeted grants do not authorize broadcasts.
	engine.HandleRequest(context.Background(), corebus.Request{
		Type:   corebus.RequestBroadcast,
		Events: []event.Event{event.New("ls", "orchestrator", nil)},
	}, "orchestrator")
	if len(chans["niq"].got) != 1 || len(chans["timer"].got) != 1 {
		t.Fatalf("broadcast leaked: niq=%d timer=%d, want 1/1", len(chans["niq"].got), len(chans["timer"].got))
	}

	// The narrowed JSON form parses in both object spellings: the shorthand
	// "target" and the canonical persistence tag "target_worker_id".
	for _, spelling := range []string{"target", "target_worker_id"} {
		var pp event.PublishPattern
		obj := `{"type":"*","` + spelling + `":"niq"}`
		if err := json.Unmarshal([]byte(obj), &pp); err != nil {
			t.Fatalf("unmarshal object: %v", err)
		}
		if pp.Type != "*" || pp.Target != "niq" {
			t.Fatalf("unmarshal object -> %+v, want {type:* target:niq}", pp)
		}
		if !pp.SendAllowed("anything", "niq") || pp.SendAllowed("anything", "workspace") {
			t.Fatal("parsed grant should allow only its named target")
		}
	}
}
