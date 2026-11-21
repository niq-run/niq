package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// replyBlock pulls the self-describing reply block out of an approval.request.
func replyBlock(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	raw, ok := payload["reply"]
	if !ok {
		t.Fatalf("approval.request payload has no reply block: %+v", payload)
	}
	// The block has to survive the bus's JSON round-trip; re-decode so the
	// assertion sees what a peer would see.
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("reply block must marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("reply block must round-trip: %v", err)
	}
	return out
}

// TestApprovalRequestCarriesReplyHint verifies both approval.request flows
// describe how to answer: the answer event, the target, the request id, the
// payload template and what each verdict does. This is what lets an automated
// approver (a reason worker's model) decide from the request alone.
func TestApprovalRequestCarriesReplyHint(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]
	reply := replyBlock(t, req.Payload)

	if reply["event"] != string(event.TypeApprovalDecision) || reply["target"] != "ws-approval" {
		t.Fatalf("reply = %+v, want event approval.decision targeting ws-approval", reply)
	}
	tmpl, ok := reply["payload_template"].(map[string]any)
	if !ok {
		t.Fatalf("reply has no payload_template: %+v", reply)
	}
	if tmpl["request_id"] != req.ID {
		t.Fatalf("template request_id = %v, want the event id %s", tmpl["request_id"], req.ID)
	}
	if _, ok := tmpl["approved"]; !ok {
		t.Fatalf("template must show the approved argument: %+v", tmpl)
	}
	hint, _ := reply["hint"].(string)
	if !strings.Contains(hint, "approval.decision") || !strings.Contains(hint, "request_id") {
		t.Fatalf("hint does not explain the answer: %q", hint)
	}
	if approved, _ := reply["approved_effect"].(string); !strings.Contains(approved, "mounted") {
		t.Fatalf("approved_effect = %q, want the mount effect", approved)
	}

	// A mount.add approval request carries the same block (different effects).
	extra := t.TempDir()
	h.ch.reset()
	h.w.handleMountAdd(event.New(TypeMountAdd, "reason-1", map[string]any{"path": extra}))
	if len(h.ch.sent) != 1 || h.ch.sent[0].Type != event.TypeApprovalRequest {
		t.Fatalf("sent = %+v, want one approval.request", h.ch.sent)
	}
	mountReply := replyBlock(t, h.ch.sent[0].Payload)
	if mountReply["target"] != "ws-approval" {
		t.Fatalf("mount reply = %+v", mountReply)
	}
	if effect, _ := mountReply["approved_effect"].(string); !strings.Contains(effect, "mount set") {
		t.Fatalf("approved_effect = %q, want the mount.add effect", effect)
	}
}

// TestDecisionAsCapabilityCall covers the automated approver's form: the
// decision arrives as a capability call (request_id in the payload, the
// caller's own call id as RequestId), so the approver gets a normal reply for
// its own call while the parked call is resolved separately.
func TestDecisionAsCapabilityCall(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]

	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{
		"request_id": req.ID, "approved": true,
	})
	decision.RequestId = "approver-call-1"
	if !h.w.DispatchExtension(decision) {
		t.Fatal("approval.decision is not a registered capability")
	}

	// The approver's own call is answered...
	answered, ok := h.ch.waitForReplyTo("approver-call-1", event.TypeRequestCompleted, testTimeout)
	if !ok {
		t.Fatalf("no request.completed for the approver's call")
	}
	if answered.Payload["request_id"] != req.ID || answered.Payload["approved"] != true || answered.Payload["resolved"] != true {
		t.Fatalf("approver reply = %+v", answered.Payload)
	}
	if mounted, _ := answered.Payload["mounted"].(string); mounted == "" {
		t.Fatalf("approver reply must name the mount: %+v", answered.Payload)
	}
	// ...and the parked call completes through the re-dispatch.
	if _, ok := h.ch.waitForReplyTo("caller-1-read", event.TypeRequestCompleted, testTimeout); !ok {
		t.Fatal("no request.completed for the parked call")
	}
	if len(h.w.parkedApprovals()) != 0 {
		t.Fatalf("pending approvals = %d, want 0", len(h.w.parkedApprovals()))
	}
}

// TestDecisionCapabilityCallErrors: a capability call must always be answered,
// even when it resolves nothing — an unanswered tool call hangs the approver.
func TestDecisionCapabilityCallErrors(t *testing.T) {
	h := newApprovalHarness(t, "approver-w")

	h.w.dispatchHandler(context.Background(), toolCall("read", "caller-1", map[string]any{"path": h.outsideFile}))
	req := h.ch.sent[0]

	cases := []struct {
		name    string
		payload map[string]any
		sender  string
		want    string
	}{
		{"unknown request", map[string]any{"request_id": "nope", "approved": true}, "approver-w", "unknown approval request"},
		{"missing verdict", map[string]any{"request_id": req.ID}, "approver-w", "approved (bool) is required"},
		{"not the approver", map[string]any{"request_id": req.ID, "approved": true}, "intruder", "not the approver"},
	}
	for i, c := range cases {
		evt := event.New(event.TypeApprovalDecision, c.sender, c.payload)
		evt.RequestId = "approver-call-" + c.name
		h.w.handleApprovalDecision(evt)
		reply, ok := h.ch.waitForReplyTo(evt.RequestId, event.TypeRequestFailed, testTimeout)
		if !ok {
			t.Fatalf("%s: no request.failed for the approver's call %d", c.name, i)
		}
		if got, _ := reply.Payload["error"].(string); !strings.Contains(got, c.want) {
			t.Fatalf("%s: error = %q, want %q", c.name, got, c.want)
		}
	}
	// None of the failed attempts consumed the request.
	if len(h.w.parkedApprovals()) != 1 {
		t.Fatalf("pending approvals = %d, want the request still parked", len(h.w.parkedApprovals()))
	}
	// The real approver still resolves it.
	decision := event.New(event.TypeApprovalDecision, "approver-w", map[string]any{
		"request_id": req.ID, "approved": false, "note": "still no",
	})
	decision.RequestId = "approver-call-final"
	h.w.handleApprovalDecision(decision)
	if _, ok := h.ch.waitForReplyTo("caller-1-read", event.TypeRequestFailed, testTimeout); !ok {
		t.Fatal("the parked call was not failed after the denial")
	}
}
