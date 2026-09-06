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
	"log"
	"os"
	"path/filepath"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
	backend "github.com/niq-run/niq/pkg/services/wsbackend"
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
	Name       string         `json:"name,omitempty"` // original tool call, if any
	Args       map[string]any `json:"args,omitempty"` // tool-call arguments, if any
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
	if w.approver == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, (&backend.EscapeError{Path: escapedPath}).Error(), tc.TraceID)
		return
	}

	_, existsErr := os.Stat(escapedPath)
	payload := jsonSafe(map[string]any{
		"origin":       tc.CallerID, // who set this in motion (reason worker / human UI)
		"requested_by": w.ID(), // the worker sending this approval request
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
	if err := w.Channel.Send(ctx, evt, w.approver); err != nil {
		log.Printf("[workspace %s] approval request to %s: %v", w.ID(), w.approver, err)
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
	}
	w.pendingMu.Unlock()
	w.NotifyDurableChange()
	log.Printf("[workspace %s] approval requested from %s: %s %s (call %s parked)",
		w.ID(), w.approver, tc.Name, escapedPath, tc.CallID)
}

// handleApprovalDecision resolves a parked operation with the approver's
// verdict. Both flows first expand the boundary (mount the path — itself if
// a directory, else its parent); they differ afterwards:
//   - with an original tool call (Name set): re-dispatch the call through the
//     normal dispatch path, which wakes it up and answers the original caller;
//   - without one (a direct mount.add request): the update itself was the
//     whole request — reply completed (with the resulting mount set) to the
//     requesting worker.
func (w *WorkspaceWorker) handleApprovalDecision(ctx context.Context, evt event.Event) {
	approved, _ := evt.Payload["approved"].(bool)
	note, _ := evt.Payload["note"].(string)

	w.pendingMu.Lock()
	pending, ok := w.pendingApprovals[evt.RequestId]
	if ok {
		delete(w.pendingApprovals, evt.RequestId)
	}
	w.pendingMu.Unlock()
	if !ok {
		log.Printf("[workspace %s] approval decision for unknown request %s (ignored)", w.ID(), evt.RequestId)
		return
	}
	w.NotifyDurableChange() // pending set changed

	if !approved {
		msg := "approval denied"
		if note != "" {
			msg += ": " + note
		}
		w.ReplyFailed(pending.CallerID, pending.CallID, msg, pending.TraceID)
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
		w.ReplyFailed(pending.CallerID, pending.CallID, "backend does not support mounts", pending.TraceID)
		return
	}
	if _, err := mm.AddMount(mountTarget); err != nil {
		log.Printf("[workspace %s] approved mount %s failed: %v", w.ID(), mountTarget, err)
		w.ReplyFailed(pending.CallerID, pending.CallID, err.Error(), pending.TraceID)
		return
	}
	log.Printf("[workspace %s] approval granted: mounted %s", w.ID(), mountTarget)
	w.NotifyDurableChange() // the mount set changed

	if pending.Name != "" {
		// Re-run the original call — now inside the expanded boundary. It
		// replies completed/failed to the original caller itself.
		w.dispatchHandler(ctx, pending.toolCall())
		return
	}
	w.ReplyCompleted(pending.CallerID, pending.CallID, mountSnapshotJSON("", mm), pending.TraceID)
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
