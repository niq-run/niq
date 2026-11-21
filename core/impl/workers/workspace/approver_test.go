package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	backend "github.com/niq-run/niq/core/impl/embedws"
	"github.com/niq-run/niq/core/itfs/event"
)

// setApprover drives the approver.set extension the way the bus would.
func (h *approvalHarness) setApprover(sender, target string) event.Event {
	evt := event.New(TypeApproverSet, sender, map[string]any{"approver": target})
	evt.RequestId = sender + "-approver-set"
	h.w.handleApproverSet(evt)
	return evt
}

// TestApproverSetDelegatesToReasonWorker covers the handover itself: after the
// current approver reassigns the role, later escape requests go to the new
// approver, and its decision resolves them.
func TestApproverSetDelegatesToReasonWorker(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	evt := h.setApprover("approver-w", "reason-auto")
	if got := h.ch.lastReplyType(); got != event.TypeRequestCompleted {
		t.Fatalf("approver.set reply = %v, want completed", got)
	}
	last := h.ch.sent[len(h.ch.sent)-1]
	if last.RequestId != evt.RequestId || last.Payload["approver"] != "reason-auto" {
		t.Fatalf("reply payload = %+v for request %s", last.Payload, last.RequestId)
	}
	if got := h.w.approverID(); got != "reason-auto" {
		t.Fatalf("approver = %q, want reason-auto", got)
	}
	if text := h.w.readmeText(); !strings.Contains(text, "- approver: reason-auto") {
		t.Fatalf("readme does not report the new approver:\n%s", text)
	}

	// A new escape asks the new approver, and its decision completes the call.
	h.ch.reset()
	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	if len(h.ch.sent) != 1 || h.ch.sent[0].Type != event.TypeApprovalRequest {
		t.Fatalf("sent = %+v, want one approval.request", h.ch.sent)
	}
	if len(h.ch.targets[0]) != 1 || h.ch.targets[0][0] != "reason-auto" {
		t.Fatalf("targets = %v, want [reason-auto]", h.ch.targets[0])
	}
	decision := event.New(event.TypeApprovalDecision, "reason-auto", map[string]any{"approved": true})
	decision.RequestId = h.ch.sent[0].RequestId
	h.w.handleApprovalDecision(decision)
	if !h.ch.waitForReplyType(event.TypeRequestCompleted, testTimeout) {
		t.Fatalf("no request.completed after the new approver approved")
	}
}

// TestApproverSetRejectsNonApprover: only the current approver may hand the
// role on, so a worker cannot make itself approver and self-approve.
func TestApproverSetRejectsNonApprover(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.setApprover("reason-1", "reason-1")

	if got := h.ch.lastReplyType(); got != event.TypeRequestFailed {
		t.Fatalf("reply = %v, want failed", got)
	}
	if got := h.w.approverID(); got != "approver-w" {
		t.Fatalf("approver = %q, want approver-w (unchanged)", got)
	}
}

// TestApproverSetRejectsSelfAssignment: pointing the role at the workspace
// itself would park every request forever (no approval handler).
func TestApproverSetRejectsSelfAssignment(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.setApprover("approver-w", "  "+h.w.ID()+" ")

	if got := h.ch.lastReplyType(); got != event.TypeRequestFailed {
		t.Fatalf("reply = %v, want failed", got)
	}
	if got := h.w.approverID(); got != "approver-w" {
		t.Fatalf("approver = %q, want approver-w (unchanged)", got)
	}
}

// TestApproverSetBootstrapsWhenNone: with no approver configured the boundary
// is ungated, so installing one only narrows it — any worker may do it.
func TestApproverSetBootstrapsWhenNone(t *testing.T) {
	h := newApprovalHarness(t, "")

	h.setApprover("reason-1", "reason-auto")

	if got := h.w.approverID(); got != "reason-auto" {
		t.Fatalf("approver = %q, want reason-auto", got)
	}
	// The gate is now live: another worker's mount.add parks instead of
	// applying directly.
	extra := t.TempDir()
	h.ch.reset()
	h.w.handleMountAdd(event.New(TypeMountAdd, "reason-2", map[string]any{"path": extra}))
	if mountsContain(h.b.Mounts(), extra) {
		t.Fatalf("mounts = %v, want %s not mounted without approval", h.b.Mounts(), extra)
	}
	if h.ch.lastTerminalReply() != "" {
		t.Fatalf("parked mount.add must not be answered yet, got %v", h.ch.lastTerminalReply())
	}
	if len(h.w.parkedApprovals()) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(h.w.parkedApprovals()))
	}
}

// TestApproverSetEmptyDisablesFlow: the approver may clear the role, which
// restores the no-approver semantics — escapes fail immediately.
func TestApproverSetEmptyDisablesFlow(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.setApprover("approver-w", "")
	if got := h.ch.lastReplyType(); got != event.TypeRequestCompleted {
		t.Fatalf("reply = %v, want completed", got)
	}
	if got := h.w.approverID(); got != "" {
		t.Fatalf("approver = %q, want empty", got)
	}

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	if len(h.ch.sent) != 2 || h.ch.sent[1].Type != event.TypeRequestFailed {
		t.Fatalf("sent = %+v, want the escape to fail immediately", h.ch.sent)
	}
	if len(h.w.parkedApprovals()) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(h.w.parkedApprovals()))
	}
}

// TestApproverSnapshotRoundtrip: a handover is durable — it comes back on
// Restore, overriding the spawn-time config; an explicit empty approver
// persists too (unlike a snapshot written before the field existed).
func TestApproverSnapshotRoundtrip(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")
	h.setApprover("approver-w", "reason-auto")

	state, err := h.w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	b2 := backend.NewEmbeddedBackend([]string{h.b.Mounts()[0]})
	ch2 := &stubChannel{}
	w2 := New(Config{ID: "ws-approval", Bus: ch2, Backend: b2, Approver: "webui"})
	if err := w2.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := w2.approverID(); got != "reason-auto" {
		t.Fatalf("restored approver = %q, want reason-auto", got)
	}

	// Clearing the approver is also persisted.
	h.setApprover("reason-auto", "")
	cleared, err := h.w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	w3 := New(Config{ID: "ws-approval", Bus: &stubChannel{}, Backend: b2, Approver: "webui"})
	if err := w3.Restore(cleared); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := w3.approverID(); got != "" {
		t.Fatalf("restored approver = %q, want empty", got)
	}

	// A snapshot without the field leaves the config's approver standing.
	w4 := New(Config{ID: "ws-approval", Bus: &stubChannel{}, Backend: b2, Approver: "webui"})
	if err := w4.Restore([]byte(`{}`)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := w4.approverID(); got != "webui" {
		t.Fatalf("approver after empty restore = %q, want webui", got)
	}
}

// TestDecisionFromNonApproverIgnored: a decision from someone who is not (and
// was not) the approver leaves the call parked for the real approver.
func TestDecisionFromNonApproverIgnored(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]

	intruder := event.New(event.TypeApprovalDecision, "intruder", map[string]any{"approved": true})
	intruder.RequestId = req.RequestId
	h.w.handleApprovalDecision(intruder)

	if len(h.w.parkedApprovals()) != 1 {
		t.Fatalf("pending approvals = %d, want the call still parked", len(h.w.parkedApprovals()))
	}
	if h.ch.lastTerminalReply() != "" {
		t.Fatalf("stray decision must not answer the call, got %v", h.ch.lastTerminalReply())
	}
	if len(h.b.Mounts()) != 1 {
		t.Fatalf("mounts = %v, want unchanged", h.b.Mounts())
	}

	// The recorded approver still resolves it.
	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{"approved": true})
	decision.RequestId = req.RequestId
	h.w.handleApprovalDecision(decision)
	if !h.ch.waitForReplyType(event.TypeRequestCompleted, testTimeout) {
		t.Fatalf("no request.completed after the approver decided")
	}
}

// TestPreviousApproverStillDecidesAfterHandover: requests already sent to the
// old approver stay decidable by it — the handover must not strand parked
// calls that a human already has in their inbox.
func TestPreviousApproverStillDecidesAfterHandover(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")
	outside2 := t.TempDir()
	outsideFile2 := filepath.Join(outside2, "f2.txt")
	if err := os.WriteFile(outsideFile2, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	first := h.ch.sent[0]
	h.setApprover("approver-w", "reason-auto")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-2", map[string]any{"path": outsideFile2}))
	second := h.ch.sent[len(h.ch.sent)-1]
	if len(h.ch.targets[len(h.ch.targets)-1]) != 1 || h.ch.targets[len(h.ch.targets)-1][0] != "reason-auto" {
		t.Fatalf("targets = %v, want the new approver", h.ch.targets)
	}

	for _, d := range []struct {
		sender string
		req    event.Event
	}{
		{"approver-w", first},   // parked before the handover
		{"reason-auto", second}, // parked after it
	} {
		decision := event.New(event.TypeApprovalDecision, d.sender, map[string]any{"approved": true})
		decision.RequestId = d.req.RequestId
		h.w.handleApprovalDecision(decision)
	}
	if len(h.w.parkedApprovals()) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(h.w.parkedApprovals()))
	}
	for _, dir := range []string{filepath.Dir(h.outsideFile), outside2} {
		if !mountsContain(h.b.Mounts(), dir) {
			t.Fatalf("mounts = %v, want %s mounted", h.b.Mounts(), dir)
		}
	}
	state, err := h.w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var s workspaceState
	if err := json.Unmarshal(state, &s); err != nil {
		t.Fatal(err)
	}
	if s.Approver == nil || *s.Approver != "reason-auto" {
		t.Fatalf("snapshot approver = %v, want reason-auto", s.Approver)
	}
}
