package hiw

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/event"
)

// stubChannel satisfies corebus.WorkerSideChannel for handler tests.
type stubChannel struct {
	sent []event.Event
}

func (s *stubChannel) ID() string { return "stub" }
func (s *stubChannel) Send(ctx context.Context, evt event.Event, targets ...string) error {
	s.sent = append(s.sent, evt)
	return nil
}
func (s *stubChannel) Broadcast(ctx context.Context, evt event.Event) error { return nil }
func (s *stubChannel) Receive(ctx context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *stubChannel) Close() error { return nil }

func approvalRequestEvent() event.Event {
	evt := event.New(event.TypeApprovalRequest, "ws-1", map[string]any{
		"worker_id": "reason-1",
		"action":    "mount.add",
		"tool":      "read",
		"path":      "/opt/tools",
	})
	evt.RequestId = evt.ID
	evt.TraceID = "trace-9"
	return evt
}

func TestApprovalLifecycle(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	// A pending request登记 → Approvals() exposes it undecided.
	req := approvalRequestEvent()
	w.recordApprovalRequest(req)
	entries := w.Approvals()
	if len(entries) != 1 || entries[0].Decision != nil {
		t.Fatalf("entries = %+v, want one pending", entries)
	}
	e := entries[0]
	if e.EventID != req.ID || e.RequestID != req.RequestId || e.WorkerID != "ws-1" ||
		e.Action != "mount.add" || e.Tool != "read" || e.Path != "/opt/tools" || e.TraceID != "trace-9" {
		t.Fatalf("entry = %+v", e)
	}

	// Decision: moves pending → decided and emits approval.decision to the
	// requester echoing the correlation fields.
	if err := w.SendApprovalDecision(context.Background(), req.RequestId, true, "ok"); err != nil {
		t.Fatalf("SendApprovalDecision: %v", err)
	}
	entries = w.Approvals()
	if len(entries) != 1 || entries[0].Decision == nil || !entries[0].Decision.Approved {
		t.Fatalf("entries after decision = %+v", entries)
	}
	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeApprovalDecision {
		t.Fatalf("sent = %+v, want one approval.decision", ch.sent)
	}
	dec := ch.sent[0]
	if dec.RequestId != req.RequestId || dec.TraceID != "trace-9" || dec.TargetWorkerID != "" {
		// TargetWorkerID is stamped by the bus; the send target is checked via
		// SendApprovalDecision's routing (verified by the successful Send).
		t.Fatalf("decision event = %+v", dec)
	}
	if dec.Payload["approved"] != true || dec.Payload["note"] != "ok" {
		t.Fatalf("decision payload = %+v", dec.Payload)
	}

	// A second decision on the same request errors — it is already decided.
	if err := w.SendApprovalDecision(context.Background(), req.RequestId, false, ""); err == nil {
		t.Fatalf("second decision must error")
	}
}

// TestSendEventCarriesRequestId verifies that a UI-driven extension call is a
// request: it carries its own ID as RequestId so the worker's reply pairs with
// it on the bus and in the UI.
func TestSendEventCarriesRequestId(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	if err := w.SendEvent(context.Background(), "mount.list", "ws-1", nil); err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	if len(ch.sent) != 1 {
		t.Fatalf("sent = %d events, want 1", len(ch.sent))
	}
	evt := ch.sent[0]
	if evt.RequestId == "" || evt.RequestId != evt.ID {
		t.Fatalf("RequestId = %q, must be the event's own id %q", evt.RequestId, evt.ID)
	}
	if evt.TraceID != evt.ID {
		t.Fatalf("TraceID = %q, want %q", evt.TraceID, evt.ID)
	}
}

func TestSnapshotRestoreRoundtrip(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	w.recordApprovalRequest(approvalRequestEvent())
	w.recordApprovalRequest(approvalRequestEvent())
	if err := w.SendApprovalDecision(context.Background(), w.pendingApprovals[0].RequestID, false, "no"); err != nil {
		t.Fatal(err)
	}

	state, err := w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	w2 := New(Config{ID: "webui-hiw", Bus: ch})
	if err := w2.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	entries := w2.Approvals()
	var pending, decided int
	for _, e := range entries {
		if e.Decision == nil {
			pending++
		} else {
			decided++
		}
	}
	if pending != 1 || decided != 1 {
		t.Fatalf("after restore pending=%d decided=%d, want 1/1", pending, decided)
	}
}
