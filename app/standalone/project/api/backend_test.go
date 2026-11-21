package api

import (
	"context"
	"testing"
)

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

// TestApprovalsRequireACapableWorker verifies the app reports a missing approval
// capability instead of pretending the inbox is empty. The mechanism itself is
// tested in core/impl/iface.
func TestApprovalsRequireACapableWorker(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch) // this stub handler serves no approvals
	if got := s.approvals(); len(got) != 0 {
		t.Fatalf("approvals = %+v, want empty", got)
	}
	if err := s.sendApprovalDecision(context.Background(), "req-1", true, ""); err == nil {
		t.Fatal("deciding on a worker without approvals succeeded")
	}
}
