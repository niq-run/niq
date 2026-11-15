// Destroy approval: a destroy is irreversible, so when the project configures
// an approver (params.destroy_approver, normally webui-hiw) the EXECUTOR — this
// host worker — raises the approval, not the caller. The call itself returns
// immediately with status=pending_approval rather than parking a bus request:
// a human decision can take minutes, and a busy agent should not be blocked on
// one. The parked destroy is durable (Snapshot/Restore) so a host restart does
// not forget an approval the human already gave.
package host

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// pendingDestroy is a destroy waiting on an approval decision.
type pendingDestroy struct {
	ApprovalID string `json:"approval_id"`
	WorkerID   string `json:"worker_id"`
	// CallerID is who asked for the removal; recorded for the approval entry
	// and the reply, not used to authorize (the bus ACL already did that).
	CallerID string `json:"caller_id,omitempty"`
	TraceID  string `json:"trace_id,omitempty"`
	// RequestedAt is informational, so the UI can age the entry.
	RequestedAt int64 `json:"requested_at,omitempty"`
}

// destroyApprover returns the configured approver id, or "" when destroy is
// not gated. params.destroy_approver names it; the boolean
// params.destroy_approval is a convenience that enables the default approver
// (webui-hiw) without naming it.
func (w *HostWorker) destroyApprover() string {
	if v := strings.TrimSpace(baseworker.ArgString(w.params, "destroy_approver")); v != "" {
		return v
	}
	if on, _ := w.params["destroy_approval"].(bool); on {
		return "webui-hiw"
	}
	return ""
}

func (w *HostWorker) handleDestroy(tc baseworker.ToolCall) (any, error) {
	id, err := workerIDArg(tc)
	if err != nil {
		return "", err
	}
	// Protection is a hard refusal, not an approval question: asking a human to
	// approve removing the host (or the WebUI's HIW) would only produce a
	// decision that can never be applied.
	if w.engine.Protected(id) {
		return "", fmt.Errorf("worker %s is protected and cannot be destroyed", id)
	}
	approver := w.destroyApprover()
	if approver == "" {
		if err := w.engine.Destroy(context.Background(), id); err != nil {
			return "", err
		}
		return map[string]any{"worker_id": id, "status": "destroyed"}, nil
	}

	evt := event.New(event.TypeApprovalRequest, w.ID(), map[string]any{
		"action":       "worker.destroy",
		"origin":       tc.CallerID,
		"requested_by": w.ID(),
		"worker_id":    id,
		"note":         "Destroying a worker removes its declaration, its persisted state and its bus identity. This cannot be undone.",
	})
	evt.RequestId = evt.ID // the decision echoes this back
	evt.TraceID = tc.TraceID
	if err := w.Channel.Send(context.Background(), evt, approver); err != nil {
		return "", err
	}
	w.pendingMu.Lock()
	w.pendingDestroy[evt.ID] = &pendingDestroy{
		ApprovalID:  evt.ID,
		WorkerID:    id,
		CallerID:    tc.CallerID,
		TraceID:     tc.TraceID,
		RequestedAt: time.Now().Unix(),
	}
	w.pendingMu.Unlock()
	w.NotifyDurableChange()

	log.Printf("[host %s] destroy of %s parked on approval %s (approver %s, caller %s)",
		w.ID(), id, evt.ID, approver, tc.CallerID)
	return map[string]any{
		"worker_id":   id,
		"status":      "pending_approval",
		"approval_id": evt.ID,
		"approver":    approver,
	}, nil
}

// registerDestroyApprovalDecl wires the approval.decision handler. It is called
// from registerExtensions so the capability is advertised like every other one.
func (w *HostWorker) registerDestroyApproval() {
	w.Register(baseworker.Extension{
		Event:       event.TypeApprovalDecision,
		Description: "Approval verdict on a destroy this host parked (approval.request with action=worker.destroy). The verdict's request id is the approval id returned to the original caller.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"approved": map[string]any{"type": "boolean", "description": "Whether the human approved the action."},
				"note":     map[string]any{"type": "string", "description": "Optional human note."},
			},
			"required": []string{"approved"},
		},
	}, w.handleApprovalDecision)
}

// handleApprovalDecision applies an approval verdict. Only destroys this host
// parked are acted on; any other decision is ignored (it belongs to another
// worker).
func (w *HostWorker) handleApprovalDecision(evt event.Event) {
	w.pendingMu.Lock()
	pending, ok := w.pendingDestroy[evt.RequestId]
	if ok {
		delete(w.pendingDestroy, evt.RequestId)
	}
	w.pendingMu.Unlock()
	if !ok {
		log.Printf("[host %s] approval decision for unknown request %s (ignored)", w.ID(), evt.RequestId)
		return
	}
	w.NotifyDurableChange()

	approved, _ := evt.Payload["approved"].(bool)
	note, _ := evt.Payload["note"].(string)
	if !approved {
		msg := "destroy denied"
		if note != "" {
			msg += ": " + note
		}
		log.Printf("[host %s] destroy of %s denied: %s", w.ID(), pending.WorkerID, note)
		w.ReplyFailed(pending.CallerID, pending.ApprovalID, msg, pending.TraceID)
		return
	}

	// The approval resolved a request the caller already saw answered (with
	// pending_approval). Destroy on its own goroutine so a slow process reap
	// cannot stall the host's event loop.
	go func() {
		if err := w.engine.Destroy(context.Background(), pending.WorkerID); err != nil {
			log.Printf("[host %s] approved destroy of %s failed: %v", w.ID(), pending.WorkerID, err)
			w.ReplyFailed(pending.CallerID, pending.ApprovalID, "destroy failed: "+err.Error(), pending.TraceID)
			return
		}
		log.Printf("[host %s] destroy of %s applied after approval", w.ID(), pending.WorkerID)
		w.ReplyCompleted(pending.CallerID, pending.ApprovalID, map[string]any{
			"worker_id": pending.WorkerID,
			"status":    "destroyed",
		}, pending.TraceID)
	}()
}
