// Boundary approval: when a tool call's path falls outside every mount, the
// worker does not merely fail — it asks its configured approver to expand the
// boundary. The pending operation is parked (the tool call stays open on the
// bus), and the approver's decision resolves it: approval mounts the path and
// re-dispatches the call, rejection fails it.
//
// The event loop must stay free while approval is pending — the decision
// arrives on the same channel — so requestApproval returns instead of
// blocking, and handleApprovalDecision completes the work.
package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/niq-run/niq/core/impl/baseworker"
	backend "github.com/niq-run/niq/core/impl/embedws"
	"github.com/niq-run/niq/core/itfs/event"
)

// pendingApproval is a parked operation awaiting the approver's decision.
// There is no explicit kind: the presence of the original tool call (Name)
// discriminates the two flows — a tool call whose path escaped every mount
// (Name set; re-dispatched on approval) versus a direct mount.add request
// from a worker other than the approver (Name empty; the update itself is
// the whole request — applied and answered on approval).
// JSON-serializable: pendings travel in the worker's Snapshot so they survive
// a restart.
type pendingApproval struct {
	ApprovalID string         `json:"approval_id"`
	Path       string         `json:"path"`
	CallerID   string         `json:"caller_id"`
	CallID     string         `json:"call_id"`
	TraceID    string         `json:"trace_id,omitempty"`
	Approver   string         `json:"approver,omitempty"` // who the request was sent to
	Name       string         `json:"name,omitempty"`     // original tool call, if any
	Args       map[string]any `json:"args,omitempty"`     // tool-call arguments, if any
}

// toolCall rebuilds the parked tool call for re-dispatch.
func (p *pendingApproval) toolCall() baseworker.ToolCall {
	return baseworker.ToolCall{
		CallID:   p.CallID,
		Name:     p.Name,
		CallerID: p.CallerID,
		Args:     p.Args,
		TraceID:  p.TraceID,
	}
}

// jsonSafe drops map entries whose values cannot be JSON-marshalled (e.g. a
// callback that some layer stashed into tool-call arguments), logging each
// drop — an unserializable value would otherwise break json.Marshal of the
// whole approval event and of every checkpoint taken while it is pending.
func jsonSafe(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if _, err := json.Marshal(v); err != nil {
			log.Printf("[workspace] approval payload: dropping non-serializable key %q (%T): %v", k, v, err)
			continue
		}
		out[k] = v
	}
	return out
}

// requestApproval parks the tool call and asks the approver to expand the
// boundary (mount the escaped path). The call is NOT answered here — it stays
// open until the approval.decision resolves it. Without an approver the call
// fails immediately (the historical behaviour).
//
// The approved object is the escaped path ITSELF — existence is not checked
// here. Approval grants scope, not existence: the path may be created later,
// and stat-ing it now would be executing part of the operation before the
// verdict (and inferred fallback targets like "the nearest existing ancestor"
// can be arbitrarily broad — mounting the home directory for a phantom
// /Users/pin/test3).
func (w *WorkspaceWorker) requestApproval(ctx context.Context, tc baseworker.ToolCall, escapedPath string) {
	approver := w.approverID()
	if approver == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, (&backend.EscapeError{Path: escapedPath}).Error(), tc.TraceID)
		return
	}

	_, existsErr := os.Stat(escapedPath)
	payload := jsonSafe(map[string]any{
		"origin":       tc.CallerID, // who set this in motion (reason worker / human UI)
		"requested_by": w.ID(),      // the worker sending this approval request
		"action":       "mount.add",
		"tool":         tc.Name,
		"path":         escapedPath,
		"exists":       existsErr == nil,
		"args":         tc.Args,
	})
	if mm, ok := w.backend.(MountManager); ok {
		payload["mounts"] = mm.Mounts() // the boundary being expanded, at request time
	}
	evt := event.New(event.TypeApprovalRequest, w.ID(), payload)
	evt.RequestId = evt.ID // the decision echoes this back
	evt.TraceID = tc.TraceID
	// Self-describing reply contract: the approver may be an automated worker
	// (a reason worker) whose model must be able to answer from the request
	// alone.
	evt.Payload["reply"] = approvalReplyHint(w.ID(), evt.ID,
		"the escaped path (its directory, for a file) is mounted and the parked "+tc.Name+" call is retried; the retried call's own reply goes to the original caller",
		"the parked "+tc.Name+" call fails with the denial note")
	if err := w.Channel.Send(ctx, evt, approver); err != nil {
		log.Printf("[workspace %s] approval request to %s: %v", w.ID(), approver, err)
		w.ReplyFailed(tc.CallerID, tc.CallID, "approval request failed: "+err.Error(), tc.TraceID)
		return
	}

	w.pendingMu.Lock()
	w.pendingApprovals[evt.ID] = &pendingApproval{
		ApprovalID: evt.ID,
		Path:       escapedPath,
		CallID:     tc.CallID,
		Name:       tc.Name,
		CallerID:   tc.CallerID,
		Args:       tc.Args,
		TraceID:    tc.TraceID,
		Approver:   approver,
	}
	w.pendingMu.Unlock()
	w.NotifyDurableChange()
	log.Printf("[workspace %s] approval requested from %s: %s %s (call %s parked)",
		w.ID(), approver, tc.Name, escapedPath, tc.CallID)
}

// approvalReplyHint is the "reply" block carried inside every
// approval.request payload. It exists because the approver may be an
// automated worker: a reason worker's model sees the payload as JSON and must
// be able to produce the decision from the request alone, without reading the
// workspace's source. The block names the answer event, the target, an exact
// payload template (with the request id filled in) and what each verdict does.
func approvalReplyHint(workspaceID, requestID, approveEffect, denyEffect string) map[string]any {
	// Reason workers expose a peer capability as <worker>__<event with dots as
	// underscores>; naming it here saves the model from mapping one to the
	// other. The capability's own description stays authoritative.
	toolName := workspaceID + "__" + strings.ReplaceAll(string(event.TypeApprovalDecision), ".", "_")
	return map[string]any{
		"event":  string(event.TypeApprovalDecision),
		"target": workspaceID,
		"payload_template": map[string]any{
			"request_id": requestID,
			"approved":   "<bool>",
			"note":       "<optional string: why, shown to the requester on denial>",
		},
		"approved_effect": approveEffect,
		"denied_effect":   denyEffect,
		"hint": fmt.Sprintf(
			"Answer by calling this worker's approval.decision capability (tool %s from a reason worker, or an approval.decision "+
				"event sent to %s) with the payload above; request_id is this request's id. One verdict resolves this request "+
				"once — do not answer the original requester, the workspace resolves the parked operation itself.", toolName, workspaceID),
	}
}

// registerApprovalDecisionExtension declares the approving side of the
// approval protocol as a CALLABLE capability, so an automated approver has a
// discoverable tool instead of having to know the protocol. The human UI's
// interface worker sends the same event as a plain directed reply (echoing the
// request id, without a request_id argument); both forms land here.
func (w *WorkspaceWorker) registerApprovalDecisionExtension() {
	w.Register(baseworker.Extension{
		Event: event.TypeApprovalDecision,
		Response: baseworker.ObjectSchema(map[string]any{
			"request_id": map[string]any{"type": "string", "description": "the approval request that was resolved"},
			"approved":   map[string]any{"type": "boolean"},
			"resolved":   map[string]any{"type": "boolean", "description": "true when a parked operation was completed by this verdict"},
		}),
		Description: "Decide one pending boundary approval of this workspace. Call it with the request_id carried by the approval.request's reply block. Approving mounts the requested path (the boundary expands) and the parked call — or the original mount.add request — completes; denying fails it with your note. Only the approver that received the request (or the workspace's current approver) may decide it; anyone else is refused and the request stays parked.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"request_id": map[string]any{"type": "string", "description": "The request id from the approval.request (its \"reply\" block repeats it)."},
			"approved":   map[string]any{"type": "boolean", "description": "true = expand the boundary by mounting the path; false = reject."},
			"note":       map[string]any{"type": "string", "description": "Optional reason; on denial it is appended to the error the requester sees."},
		}, "required": []any{"request_id", "approved"}},
	}, w.handleApprovalDecision)
}

// handleApprovalDecision resolves a parked operation with the approver's
// verdict. It serves both wire forms:
//   - a capability call (what an automated approver does): the payload carries
//     request_id, the event's own RequestId is the CALLER's call id, and the
//     caller gets a request.completed/failed for its call;
//   - a plain directed reply (what the human UI's worker does): payload
//     {approved, note}, RequestId = the approval request, no answer expected.
//
// Both flows first expand the boundary (mount the path — itself if a
// directory, else its parent); they differ afterwards:
//   - with an original tool call (Name set): re-dispatch the call through the
//     normal dispatch path, which wakes it up and answers the original caller;
//   - without one (a direct mount.add request): the update itself was the
//     whole request — reply completed (with the resulting mount set) to the
//     requesting worker.
func (w *WorkspaceWorker) handleApprovalDecision(evt event.Event) {
	approved, hasVerdict := evt.Payload["approved"].(bool)
	note, _ := evt.Payload["note"].(string)
	requestID, _ := evt.Payload["request_id"].(string)
	// A capability call carries request_id and waits for an answer; a plain
	// protocol reply only echoes the request id.
	toolCall := requestID != ""
	answer := func(err error, result map[string]any) {
		if !toolCall {
			return
		}
		if err != nil {
			w.ReplyFailed(evt.WorkerId, evt.RequestId, err.Error(), evt.TraceID)
			return
		}
		w.ReplyCompleted(evt.WorkerId, evt.RequestId, result, evt.TraceID)
	}
	if !toolCall {
		requestID = evt.RequestId
	}
	if !hasVerdict {
		answer(errors.New("approved (bool) is required"), nil)
		return
	}

	w.pendingMu.Lock()
	pending, ok := w.pendingApprovals[requestID]
	w.pendingMu.Unlock()
	if !ok {
		log.Printf("[workspace %s] approval decision for unknown request %s from %s (ignored)", w.ID(), requestID, evt.WorkerId)
		answer(fmt.Errorf("unknown approval request %q: it was already decided, or never existed", requestID), nil)
		return
	}
	// Only the approver that received the request — or the current one, after
	// a handover — may decide it. A stray decision leaves the call parked
	// instead of consuming it.
	if !w.mayDecide(evt.WorkerId, pending) {
		log.Printf("[workspace %s] approval decision for %s from %q ignored: not the approver",
			w.ID(), requestID, evt.WorkerId)
		answer(fmt.Errorf("not the approver of %s: only %q (or the current approver) may decide it",
			requestID, pending.Approver), nil)
		return
	}
	w.pendingMu.Lock()
	delete(w.pendingApprovals, requestID)
	w.pendingMu.Unlock()
	w.NotifyDurableChange() // pending set changed

	if !approved {
		msg := "approval denied"
		if note != "" {
			msg += ": " + note
		}
		w.ReplyFailed(pending.CallerID, pending.CallID, msg, pending.TraceID)
		answer(nil, map[string]any{"request_id": requestID, "approved": false, "resolved": true})
		return
	}

	// Approved object = the escaped path itself (what the approver saw is
	// what gets mounted). An existing file mounts its container directory —
	// mounts are directory-scoped — and a not-yet-existing path mounts as a
	// phantom entry (validateMount accepts it): operations on it report ENOENT
	// until the path is created, after which it is simply inside the boundary.
	mountTarget := pending.Path
	if info, err := os.Stat(pending.Path); err == nil && !info.IsDir() {
		mountTarget = filepath.Dir(pending.Path)
	}
	mm, ok := w.backend.(MountManager)
	if !ok {
		err := errors.New("backend does not support mounts")
		w.ReplyFailed(pending.CallerID, pending.CallID, err.Error(), pending.TraceID)
		answer(err, nil)
		return
	}
	if _, err := mm.AddMount(mountTarget); err != nil {
		log.Printf("[workspace %s] approved mount %s failed: %v", w.ID(), mountTarget, err)
		w.ReplyFailed(pending.CallerID, pending.CallID, err.Error(), pending.TraceID)
		answer(err, nil)
		return
	}
	log.Printf("[workspace %s] approval granted: mounted %s", w.ID(), mountTarget)
	w.NotifyDurableChange() // the mount set changed
	answer(nil, map[string]any{
		"request_id": requestID,
		"approved":   true,
		"resolved":   true,
		"mounted":    mountTarget,
	})

	if pending.Name != "" {
		// Re-run the original call — now inside the expanded boundary. It
		// replies completed/failed to the original caller itself. Served on its
		// own goroutine (like every tool dispatch) so re-running a long command
		// here cannot stall the event loop.
		go w.dispatchHandler(context.Background(), pending.toolCall())
		return
	}
	w.ReplyCompleted(pending.CallerID, pending.CallID, mountSnapshot("", mm), pending.TraceID)
}

// parkedApprovals snapshots the pending set for Snapshot.
func (w *WorkspaceWorker) parkedApprovals() []pendingApproval {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	out := make([]pendingApproval, 0, len(w.pendingApprovals))
	for _, p := range w.pendingApprovals {
		out = append(out, *p)
	}
	return out
}

// restoreParkedApprovals rehydrates the pending set from a Snapshot before
// Start; later decisions resolve them normally.
func (w *WorkspaceWorker) restoreParkedApprovals(items []pendingApproval) {
	w.pendingMu.Lock()
	defer w.pendingMu.Unlock()
	w.pendingApprovals = make(map[string]*pendingApproval, len(items))
	for i := range items {
		w.pendingApprovals[items[i].ApprovalID] = &items[i]
	}
}
