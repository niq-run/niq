package workspace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
	backend "github.com/niq-run/niq/pkg/services/wsbackend"
)

// approvalHarness builds a worker over a single-mount backend with a stub
// channel, plus an out-of-mount directory containing a file.
type approvalHarness struct {
	w           *WorkspaceWorker
	b           *backend.EmbeddedBackend
	ch          *stubChannel
	outsideFile string
}

func newApprovalHarness(t *testing.T, approver string) *approvalHarness {
	t.Helper()
	primary := t.TempDir()
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "f.txt")
	if err := os.WriteFile(outsideFile, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	b := backend.NewEmbeddedBackend([]string{primary})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-approval", Bus: ch, Backend: b, Approver: approver})
	w.buildHandlers() // no Start(): assemble the handler map directly
	return &approvalHarness{w: w, b: b, ch: ch, outsideFile: outsideFile}
}

func toolCall(name, caller string, args map[string]any) baseworker.ToolCall {
	return baseworker.ToolCall{CallID: caller + "-" + name, Name: name, CallerID: caller, Args: args, TraceID: "trace-1"}
}

func mountsContain(mounts []string, dir string) bool {
	for _, m := range mounts {
		if m == dir {
			return true
		}
	}
	return false
}

// lastTerminalReply returns the most recent request.completed/failed/rejected
// reply ("" when none), ignoring non-answer traffic like the approval.request.
func (s *stubChannel) lastTerminalReply() event.EventType {
	for i := len(s.sent) - 1; i >= 0; i-- {
		switch s.sent[i].Type {
		case event.TypeRequestCompleted, event.TypeRequestFailed, event.TypeRequestRejected:
			return s.sent[i].Type
		}
	}
	return ""
}

func TestRequestApprovalParksCall(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))

	// No terminal reply for the call — it is parked.
	if h.ch.lastTerminalReply() != "" {
		t.Fatalf("parked call must not be answered yet, got %v", h.ch.lastTerminalReply())
	}
	// Exactly one directed approval.request to the approver.
	if len(h.ch.sent) != 1 || h.ch.sent[0].Type != event.TypeApprovalRequest {
		t.Fatalf("sent = %+v, want one approval.request", h.ch.sent)
	}
	req := h.ch.sent[0]
	if len(h.ch.targets[0]) != 1 || h.ch.targets[0][0] != "approver-w" {
		t.Fatalf("targets = %v, want [approver-w]", h.ch.targets[0])
	}
	if req.RequestId == "" || req.RequestId != req.ID {
		t.Fatalf("RequestId must be the event's own id, got %q", req.RequestId)
	}
	if req.Payload["path"] != h.outsideFile || req.Payload["action"] != "mount.add" ||
		req.Payload["origin"] != "caller-1" || req.Payload["requested_by"] != "ws-approval" {
		t.Fatalf("payload = %+v", req.Payload)
	}
	// The payload must be JSON-marshalable: the bus persists events via
	// json.Marshal, and a marshal failure silently stores an empty payload.
	if _, err := json.Marshal(req.Payload); err != nil {
		t.Fatalf("payload must marshal: %v", err)
	}
	if len(h.w.parkedApprovals()) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(h.w.parkedApprovals()))
	}
}

func TestApprovalDecisionApproved(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]

	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{"approved": true})
	decision.RequestId = req.RequestId
	h.w.handleApprovalDecision(context.Background(), decision)

	// The escaped path's directory is now mounted and the call completed.
	if !mountsContain(h.b.Mounts(), filepath.Dir(h.outsideFile)) {
		t.Fatalf("mounts = %v, want %s", h.b.Mounts(), filepath.Dir(h.outsideFile))
	}
	last := h.ch.sent[len(h.ch.sent)-1]
	if last.Type != event.TypeRequestCompleted || last.RequestId != "caller-1-read" {
		t.Fatalf("reply = %v request %s, want completed for the parked call", last.Type, last.RequestId)
	}
	if len(h.w.parkedApprovals()) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(h.w.parkedApprovals()))
	}
}

func TestApprovalDecisionRejected(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]
	mountsBefore := len(h.b.Mounts())

	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{"approved": false, "note": "no"})
	decision.RequestId = req.RequestId
	h.w.handleApprovalDecision(context.Background(), decision)

	last := h.ch.sent[len(h.ch.sent)-1]
	if last.Type != event.TypeRequestFailed || last.RequestId != "caller-1-read" {
		t.Fatalf("reply = %v request %s, want failed for the parked call", last.Type, last.RequestId)
	}
	if len(h.b.Mounts()) != mountsBefore {
		t.Fatalf("mounts must be unchanged, got %v", h.b.Mounts())
	}
}

func TestNoApproverFailsImmediately(t *testing.T) {
	h := newApprovalHarness(t, "")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))

	if len(h.ch.sent) != 1 || h.ch.sent[0].Type != event.TypeRequestFailed {
		t.Fatalf("sent = %+v, want a single request.failed", h.ch.sent)
	}
	if len(h.w.parkedApprovals()) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(h.w.parkedApprovals()))
	}
}

func TestModeSetRejectsWrites(t *testing.T) {
	primary := t.TempDir()
	b := backend.NewEmbeddedBackend([]string{primary})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-mode", Bus: ch, Backend: b})
	w.buildHandlers()

	file := filepath.Join(primary, "f.txt")
	setMode := func(mode string) {
		w.handleModeSet(event.New(TypeModeSet, "caller", map[string]any{"mode": mode}))
	}
	setMode(ModeNameReadOnly)

	// Write is rejected with the read-only error; read-class tools still work.
	w.dispatchHandler(context.Background(), toolCall("write", "caller-1", map[string]any{"path": file, "content": "x"}))
	if last := ch.sent[len(ch.sent)-1]; last.Type != event.TypeRequestFailed {
		t.Fatalf("write in readonly = %v, want failed", last.Type)
	}

	w.dispatchHandler(context.Background(), toolCall("ls", "caller-2", map[string]any{"path": primary}))
	if last := ch.sent[len(ch.sent)-1]; last.Type != event.TypeRequestCompleted {
		t.Fatalf("ls in readonly = %v, want completed", last.Type)
	}

	// Tools stay declared: the mode extension plus all tool extensions remain
	// registered (readonly is a runtime flag, not an unregistration).
	declared := map[string]bool{}
	for _, ext := range w.Extensions() {
		declared[string(ext.Event)] = true
	}
	for _, tool := range []string{"read", "write", "edit", "bash"} {
		if !declared[tool] {
			t.Fatalf("tool %q must stay declared in readonly mode", tool)
		}
	}

	setMode(ModeNameReadWrite)
	w.dispatchHandler(context.Background(), toolCall("write", "caller-3", map[string]any{"path": file, "content": "x"}))
	if last := ch.sent[len(ch.sent)-1]; last.Type != event.TypeRequestCompleted {
		t.Fatalf("write after readwrite = %v, want completed", last.Type)
	}
}

func TestMountAddFromApproverAppliesDirectly(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")
	extra := t.TempDir()

	// The approver's own request is its own consent: applied immediately.
	h.w.handleMountAdd(event.New(TypeMountAdd, "approver-w", map[string]any{"path": extra}))

	if !mountsContain(h.b.Mounts(), extra) {
		t.Fatalf("mounts = %v, want %s", h.b.Mounts(), extra)
	}
	last := h.ch.sent[len(h.ch.sent)-1]
	if last.Type != event.TypeRequestCompleted {
		t.Fatalf("reply = %v, want completed", last.Type)
	}
	if len(h.w.parkedApprovals()) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(h.w.parkedApprovals()))
	}
}

func TestMountAddFromOtherWorkerWaitsForApproval(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")
	extra := t.TempDir()

	// A non-approver's request parks and asks the approver.
	h.w.handleMountAdd(event.New(TypeMountAdd, "reason-1", map[string]any{"path": extra}))
	if h.ch.lastTerminalReply() != "" {
		t.Fatalf("parked request must not be answered yet, got %v", h.ch.lastTerminalReply())
	}
	if len(h.ch.sent) != 1 || h.ch.sent[0].Type != event.TypeApprovalRequest {
		t.Fatalf("sent = %+v, want one approval.request", h.ch.sent)
	}
	if len(h.ch.targets[0]) != 1 || h.ch.targets[0][0] != "approver-w" {
		t.Fatalf("targets = %v, want [approver-w]", h.ch.targets[0])
	}
	if payload := h.ch.sent[0].Payload; payload["action"] != "mount.add" || payload["path"] != extra || payload["origin"] != "reason-1" || payload["requested_by"] != "ws-approval" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(h.w.parkedApprovals()) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(h.w.parkedApprovals()))
	}

	// Approval applies the mount and answers the requester.
	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{"approved": true})
	decision.RequestId = h.ch.sent[0].RequestId
	h.w.handleApprovalDecision(context.Background(), decision)
	if !mountsContain(h.b.Mounts(), extra) {
		t.Fatalf("mounts = %v, want %s", h.b.Mounts(), extra)
	}
	last := h.ch.sent[len(h.ch.sent)-1]
	if last.Type != event.TypeRequestCompleted {
		t.Fatalf("reply = %v, want completed", last.Type)
	}
	var result struct {
		Mounts []string `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(last.Payload["result"].(string)), &result); err != nil || !mountsContain(result.Mounts, extra) {
		t.Fatalf("result = %v", last.Payload["result"])
	}
}

func TestMountAddFromOtherWorkerDenied(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")
	extra := t.TempDir()
	mountsBefore := len(h.b.Mounts())

	h.w.handleMountAdd(event.New(TypeMountAdd, "reason-1", map[string]any{"path": extra}))
	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{"approved": false, "note": "no"})
	decision.RequestId = h.ch.sent[0].RequestId
	h.w.handleApprovalDecision(context.Background(), decision)

	last := h.ch.sent[len(h.ch.sent)-1]
	if last.Type != event.TypeRequestFailed {
		t.Fatalf("reply = %v, want failed", last.Type)
	}
	if len(h.b.Mounts()) != mountsBefore {
		t.Fatalf("mounts must be unchanged, got %v", h.b.Mounts())
	}
}

func TestSnapshotCarriesModeAndPendings(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]
	h.w.handleModeSet(event.New(TypeModeSet, "caller", map[string]any{"mode": ModeNameReadOnly}))

	state, err := h.w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var s workspaceState
	if err := json.Unmarshal(state, &s); err != nil {
		t.Fatal(err)
	}
	if s.Mode != ModeNameReadOnly {
		t.Fatalf("snapshot mode = %q, want readonly", s.Mode)
	}
	if len(s.PendingApprovals) != 1 {
		t.Fatalf("snapshot pendings = %d, want 1", len(s.PendingApprovals))
	}

	// A fresh worker restores mode + parked approvals; the decision then
	// resolves across the "restart".
	b2 := backend.NewEmbeddedBackend([]string{h.b.Mounts()[0]})
	ch2 := &stubChannel{}
	w2 := New(Config{ID: "ws-approval", Bus: ch2, Backend: b2, Approver: "approver-w"})
	w2.buildHandlers()
	if err := w2.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(w2.parkedApprovals()) != 1 {
		t.Fatalf("restored pendings = %d, want 1", len(w2.parkedApprovals()))
	}
	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{"approved": true})
	decision.RequestId = req.RequestId
	w2.handleApprovalDecision(context.Background(), decision)
	if !mountsContain(b2.Mounts(), filepath.Dir(h.outsideFile)) {
		t.Fatalf("restored worker did not mount %s: %v", filepath.Dir(h.outsideFile), b2.Mounts())
	}
	if last := ch2.sent[len(ch2.sent)-1]; last.Type != event.TypeRequestCompleted {
		t.Fatalf("reply after restore = %v, want completed", last.Type)
	}
}
