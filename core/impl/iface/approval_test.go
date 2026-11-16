package iface

import (
	"context"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestApprovalsOffByDefault verifies the feature is opt-in: without the flag the
// worker does not declare the capability and its handle carries no inbox.
func TestApprovalsOffByDefault(t *testing.T) {
	be := newFakeBackend()
	_, p := startWorker(t, Config{ID: "w1", Backends: []Backend{be}})

	if _, ok := be.handler(t).(Inbox); ok {
		t.Fatal("a worker without Approvals exposes an inbox")
	}
	// No extension was registered, so an approval.request is not a served tool: it
	// fans out to the backends as an ordinary message.
	var mu chan struct{} = make(chan struct{}, 1)
	be.handler(t).OnEvent(func(e event.Event) {
		if e.Type == event.TypeApprovalRequest {
			select {
			case mu <- struct{}{}:
			default:
			}
		}
	}, event.TypeApprovalRequest)
	p.push(event.New(event.TypeApprovalRequest, "workspace", map[string]any{"action": "mount.add"}))
	select {
	case <-mu:
	case <-time.After(2 * time.Second):
		t.Fatal("approval.request did not reach the backend")
	}
}

// TestApprovalsInboxFlow verifies the whole mechanism: the capability is declared
// in the watch contract, a request is parked, the inbox reports it, and a verdict
// is published to the requester as this worker with the request/trace echoed.
func TestApprovalsInboxFlow(t *testing.T) {
	be := newFakeBackend()
	w, p := startWorker(t, Config{ID: "w1", Type: "interface", Backends: []Backend{be}, Approvals: true})

	// Declared: the watch contract advertises approval.request with a schema, so a
	// peer or the directory can discover who approves.
	var advertised bool
	for _, e := range w.WatchEntries(false) {
		if e["event"] != string(event.TypeApprovalRequest) {
			continue
		}
		advertised = true
		if _, ok := e["parameters"]; !ok {
			t.Fatalf("approval.request advertised without a schema: %+v", e)
		}
	}
	if !advertised {
		t.Fatalf("approval.request missing from the watch contract: %+v", w.WatchEntries(false))
	}

	in, ok := be.handler(t).(Inbox)
	if !ok {
		t.Fatal("handle does not expose the inbox")
	}

	changed := make(chan struct{}, 4)
	in.OnChange(func() { changed <- struct{}{} })

	req := event.New(event.TypeApprovalRequest, "workspace", map[string]any{
		"action": "mount.add", "tool": "mount.add", "path": "/etc",
	})
	req.RequestId = "req-1"
	req.TraceID = "trace-1"
	p.push(req)

	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("OnChange did not fire for a new request")
	}
	pending := in.Pending()
	if len(pending) != 1 {
		t.Fatalf("pending = %+v, want one entry", pending)
	}
	e := pending[0]
	if e.RequestID != "req-1" || e.WorkerID != "workspace" || e.Action != "mount.add" || e.Path != "/etc" {
		t.Fatalf("entry = %+v", e)
	}
	// Parking must not answer the request: the decision IS the reply.
	if last, ok := p.lastSent(); ok && last.Type == event.TypeRequestCompleted {
		t.Fatal("approval.request was answered with request.completed")
	}

	if err := in.Decide(context.Background(), "req-1", false, "not that one"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	sent, ok := p.lastSent()
	if !ok || sent.Type != event.TypeApprovalDecision {
		t.Fatalf("last sent = %+v, want approval.decision", sent)
	}
	if sent.RequestId != "req-1" || sent.TraceID != "trace-1" || sent.WorkerId != "w1" {
		t.Fatalf("decision = %+v (request/trace/from must echo)", sent)
	}
	if len(p.target) == 0 || p.target[len(p.target)-1] != "workspace" {
		t.Fatalf("decision targets = %v, want the requester", p.target)
	}
	if sent.Payload["approved"] != false || sent.Payload["note"] != "not that one" {
		t.Fatalf("decision payload = %+v", sent.Payload)
	}

	if len(in.Pending()) != 0 || len(in.Decided()) != 1 {
		t.Fatalf("pending=%d decided=%d, want 0/1", len(in.Pending()), len(in.Decided()))
	}
	if err := in.Decide(context.Background(), "req-1", true, ""); err == nil {
		t.Fatal("re-deciding a settled request succeeded")
	}
}

// TestApprovalsDoNotLeakIntoBackendTraffic verifies a parked request is consumed
// by the capability instead of being fanned out as a message: the backend sees
// messages, and reads approvals from the inbox.
func TestApprovalsDoNotLeakIntoBackendTraffic(t *testing.T) {
	be := newFakeBackend()
	_, p := startWorker(t, Config{ID: "w1", Backends: []Backend{be}, Approvals: true})

	var mu = make(chan event.Event, 4)
	be.handler(t).OnEvent(func(e event.Event) { mu <- e })
	p.push(event.New(event.TypeApprovalRequest, "workspace", map[string]any{"action": "mount.add"}))
	p.push(event.New(event.TypeWorkerInput, "reason", nil))

	select {
	case e := <-mu:
		if e.Type == event.TypeApprovalRequest {
			t.Fatal("approval.request reached the backend as a message; it is a served capability")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker.input never reached the backend")
	}
}

// TestInboxSurvivesASnapshot verifies recovery is a SNAPSHOT: the inbox round
// trips, pending stays pending (so the request is still answerable), decided stays
// decided, and the change signal fires when the inbox moves.
func TestInboxSurvivesASnapshot(t *testing.T) {
	var durable int
	w := New(Config{ID: "w1", Bus: newPipe(), Approvals: true, OnDurableChange: func() { durable++ }})
	in, ok := w.handle.(Inbox)
	if !ok {
		t.Fatal("handle does not expose the inbox")
	}

	req := event.New(event.TypeApprovalRequest, "workspace", map[string]any{
		"action": "mount.add", "tool": "read", "path": "/etc/hosts",
	})
	req.RequestId = "req-1"
	w.inbox.record(req)
	if durable != 1 {
		t.Fatalf("durable signals = %d, want 1 after parking", durable)
	}

	snap, err := w.Snapshot()
	if err != nil || len(snap) == 0 {
		t.Fatalf("Snapshot = %q, %v", snap, err)
	}

	restored := New(Config{ID: "w1", Bus: newPipe(), Approvals: true})
	if err := restored.Restore(snap); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	pending := restored.Inbox().Pending()
	if len(pending) != 1 {
		t.Fatalf("pending after restore = %+v", pending)
	}
	if pending[0].RequestID != "req-1" || pending[0].Path != "/etc/hosts" || pending[0].WorkerID != "workspace" {
		t.Fatalf("restored entry = %+v", pending[0])
	}
	// Still answerable: that is the whole point of restoring a pending request.
	if err := restored.Inbox().Decide(context.Background(), "req-1", true, "ok"); err != nil {
		t.Fatalf("Decide after restore: %v", err)
	}
	if len(in.Decided())+len(restored.Inbox().Decided()) == 0 {
		t.Fatal("decision did not move the entry")
	}
}
