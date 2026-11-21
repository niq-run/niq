// Approver assignment: who the workspace asks before expanding its boundary.
//
// The approver is seeded from config at spawn (the app defaults it to the human
// UI worker) and can be handed over at runtime through approver.set — e.g. to a
// reason worker that decides out-of-mount requests itself instead of a human
// approving each one. The protocol is unchanged: the approver is only an
// address that approval.request is sent to.
//
// Handover is gated so it cannot be used to self-approve: once an approver is
// configured, only that approver may reassign the role (delegation). With no
// approver configured, anybody may install one — that only ever narrows the
// gate (an empty approver means mount.add applies directly and escapes fail),
// so it grants no capability the caller did not already have.
package workspace

import (
	"fmt"
	"log"
	"strings"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// TypeApproverSet is the approver-handover extension event.
const TypeApproverSet event.EventType = "approver.set"

func (w *WorkspaceWorker) registerApproverExtension() {
	w.Register(baseworker.Extension{
		Event: TypeApproverSet,
		Response: baseworker.ObjectSchema(map[string]any{
			"approver": map[string]any{"type": "string", "description": "the worker now receiving approval requests (empty = none)"},
		}),
		Description: "Hand the boundary-approval role to another worker — e.g. a reason worker that decides out-of-mount requests itself, so a human no longer approves each one. Only the current approver may reassign it (delegation); when no approver is configured any worker may install one, which only narrows the gate. An empty approver disables the flow: boundary escapes fail immediately and mount.add applies directly. Pending requests keep the approver they were sent to.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"approver": map[string]any{"type": "string", "description": "Worker id to send approval.request to from now on; empty string removes the approver."},
			"reason":   map[string]any{"type": "string", "description": "Optional note for the log explaining the handover (not validated, not forwarded)."},
		}, "required": []any{"approver"}},
	}, w.handleApproverSet)
}

// handleApproverSet reassigns the approver. The role is a durable attribute
// (Snapshot), so a handover survives a restart; requests already parked keep
// the approver they were sent to and stay answerable by it.
func (w *WorkspaceWorker) handleApproverSet(evt event.Event) {
	target, ok := evt.Payload["approver"].(string)
	if !ok {
		w.ReplyFailed(evt.WorkerId, evt.RequestId,
			`approver is required (a worker id, or "" to disable the flow)`, evt.TraceID)
		return
	}
	target = strings.TrimSpace(target)
	if target == w.ID() {
		// The workspace cannot approve itself: it has no approval handler, so
		// the request would park forever.
		w.ReplyFailed(evt.WorkerId, evt.RequestId,
			"the workspace cannot be its own approver", evt.TraceID)
		return
	}
	w.mu.Lock()
	current := w.approver
	if current != "" && evt.WorkerId != current {
		w.mu.Unlock()
		w.ReplyFailed(evt.WorkerId, evt.RequestId,
			fmt.Sprintf("only the current approver (%s) may reassign it", current), evt.TraceID)
		return
	}
	w.approver = target
	w.mu.Unlock()

	reason, _ := evt.Payload["reason"].(string)
	if reason != "" {
		log.Printf("[workspace %s] approver %q -> %q (by %s): %s", w.ID(), current, target, evt.WorkerId, reason)
	} else {
		log.Printf("[workspace %s] approver %q -> %q (by %s)", w.ID(), current, target, evt.WorkerId)
	}
	w.NotifyDurableChange()
	w.ReplyCompleted(evt.WorkerId, evt.RequestId, map[string]any{"approver": target}, evt.TraceID)
}

// approverID reads the configured approver. It is guarded by w.mu because tool
// dispatch runs on goroutines while approver.set lands on the event loop (mode
// is read the same way).
func (w *WorkspaceWorker) approverID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.approver
}

// mayDecide reports whether senderID is entitled to decide a parked approval:
// the approver the request was sent to (still valid after a handover — that
// approver already holds the request) or the current one. Pendings restored
// from an older snapshot carry no approver, so the current approver decides.
func (w *WorkspaceWorker) mayDecide(senderID string, p *pendingApproval) bool {
	if senderID == "" {
		return false
	}
	if p.Approver != "" && senderID == p.Approver {
		return true
	}
	return senderID == w.approverID()
}
