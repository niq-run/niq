package webui

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// TestApprovalInboxTracksAndAnswers verifies the human inbox that used to live in
// the hiw worker: a request addressed to this identity is parked, listed, and
// answered with approval.decision sent back to the requester — as this worker.
func TestApprovalInboxTracksAndAnswers(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	req := event.New(event.TypeApprovalRequest, "workspace", map[string]any{
		"action": "mount.add", "tool": "mount.add", "path": "/etc",
	})
	req.RequestId = "req-1"
	req.TraceID = "trace-1"
	s.recordApprovalRequest(req)

	entries := s.approvals()
	if len(entries) != 1 {
		t.Fatalf("approvals = %+v, want one pending entry", entries)
	}
	e := entries[0]
	if e.RequestID != "req-1" || e.WorkerID != "workspace" || e.Action != "mount.add" || e.Path != "/etc" {
		t.Fatalf("entry = %+v", e)
	}

	if err := s.sendApprovalDecision(context.Background(), "req-1", false, "not that one"); err != nil {
		t.Fatalf("decision: %v", err)
	}
	if len(ch.sent) != 1 {
		t.Fatalf("sent = %d events, want the decision only", len(ch.sent))
	}
	got := ch.sent[0]
	if got.Type != event.TypeApprovalDecision {
		t.Fatalf("sent %s, want approval.decision", got.Type)
	}
	if got.RequestId != "req-1" || got.TraceID != "trace-1" || got.WorkerId != "webui" {
		t.Fatalf("decision event = %+v (request/trace/from must echo)", got)
	}
	if len(ch.targets) != 1 || ch.targets[0][0] != "workspace" {
		t.Fatalf("decision targets = %v, want the requester", ch.targets)
	}
	if got.Payload["approved"] != false || got.Payload["note"] != "not that one" {
		t.Fatalf("decision payload = %+v", got.Payload)
	}

	// The entry moved to the decided history, and re-answering it is an error
	// (the UI polls a snapshot that may have moved on).
	entries = s.approvals()
	if len(entries) != 1 || entries[0].Decision == nil || entries[0].Decision.Approved {
		t.Fatalf("decided entry = %+v", entries)
	}
	if err := s.sendApprovalDecision(context.Background(), "req-1", true, ""); err == nil {
		t.Fatal("re-answering a decided request succeeded")
	}
}

// TestBusWriteRequiresTheWorker verifies a Server that has not been injected yet
// reports the actionable error instead of panicking on a nil handler.
func TestBusWriteRequiresTheWorker(t *testing.T) {
	s := &Server{}
	if err := s.sendInput(context.Background(), nil, []string{"a"}, ""); err == nil {
		t.Fatal("sendInput succeeded without a bus handler")
	}
	if _, err := s.sendEvent(context.Background(), "worker.abort", "a", nil); err == nil {
		t.Fatal("sendEvent succeeded without a bus handler")
	}
	if _, err := s.callHost(context.Background(), "resume", map[string]any{"worker_id": "x"}); err == nil {
		t.Fatal("callHost succeeded without a bus handler")
	}
}
