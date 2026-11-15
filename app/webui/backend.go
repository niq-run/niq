package webui

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/niq-run/niq/core/impl/iface"
	"github.com/niq-run/niq/core/itfs/event"
)

// The WebUI is the backend of its interface worker: the worker faces the bus,
// the app faces the human. SetHandler is the injection point — it arrives before
// the worker starts, which is exactly when the app needs to know how to reach
// inward.
//
// Everything the app does on the bus goes through this one handle: sending input
// as the human, asking a worker a question, answering an approval. Reads that are
// not about "who am I on the bus" (the global event stream, the identity table)
// stay on the app's own bus handles; routing them through the worker would add a
// hop and hide what they are.

// SetHandler implements iface.Backend.
func (s *Server) SetHandler(h iface.Handler) {
	s.busMu.Lock()
	s.bus = h
	s.busMu.Unlock()

	// Approval requests are addressed to this worker; they land here as an inbox
	// the human answers.
	h.OnEvent(s.recordApprovalRequest, event.TypeApprovalRequest)
	log.Printf("[webui] joined the bus as %s", h.ID())
}

// busHandler returns the injected handler, or an error explaining that the
// worker is not on the bus yet (the UI stays usable for reading, but nothing can
// be sent until it is).
func (s *Server) busHandler() (iface.Handler, error) {
	s.busMu.RLock()
	defer s.busMu.RUnlock()
	if s.bus == nil {
		return nil, fmt.Errorf("the webui worker is not on the bus; retry once it is back")
	}
	return s.bus, nil
}

// ── The human's voice ──

// sendInput publishes a user message as this worker — the human's voice on the
// bus. Targets lists every worker the message is addressed to; an empty list
// broadcasts (callers normally require at least one @).
//
// The payload carries its content as a "content" array of llm-like blocks
// ({type: text|image|file}); the reason input converter interprets the shape, and
// the bus treats it as an ordinary payload.
func (s *Server) sendInput(ctx context.Context, content []map[string]any, targets []string, mode string) error {
	h, err := s.busHandler()
	if err != nil {
		return err
	}
	payload := map[string]any{}
	if len(content) > 0 {
		payload["content"] = content
	}
	if mode != "" && mode != "default" {
		payload["input_mode"] = mode
	}
	evt := event.New(event.TypeWorkerInput, h.ID(), payload)
	evt.TraceID = evt.ID
	if len(targets) > 0 {
		return h.Send(context.Background(), evt, targets...)
	}
	return h.Broadcast(context.Background(), evt)
}

// sendEvent publishes an arbitrary event as the human: driving a worker with one
// of the events it declared it responds to. It returns the request id (the
// event's own id) so the caller can pair the worker's reply with the card that
// raised it. The bus ACL still gates it — this identity may only publish what it
// has been granted.
func (s *Server) sendEvent(ctx context.Context, evtType event.EventType, target string, payload map[string]any) (string, error) {
	h, err := s.busHandler()
	if err != nil {
		return "", err
	}
	evt := event.New(evtType, h.ID(), payload)
	evt.TraceID = evt.ID
	evt.RequestId = evt.ID
	if err := h.Send(ctx, evt, target); err != nil {
		return "", err
	}
	return evt.ID, nil
}

// ── Approvals ──

// ApprovalDecision is the human's verdict on one approval request.
type ApprovalDecision struct {
	Approved  bool   `json:"approved"`
	Note      string `json:"note,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// ApprovalEntry is one approval request this worker tracks for the UI.
type ApprovalEntry struct {
	EventID   string `json:"id"`             // approval.request event id
	RequestID string `json:"request_id"`     // correlation key (= its RequestId)
	WorkerID  string `json:"worker_id"`      // requester (e.g. workspace worker)
	Action    string `json:"action"`         // e.g. "mount.add"
	Tool      string `json:"tool,omitempty"` // tool that raised the request
	Path      string `json:"path,omitempty"` // boundary-expansion target
	TraceID   string `json:"trace_id,omitempty"`
	Timestamp int64  `json:"timestamp"`
	// Payload is the approval request's full payload, carried through for the UI
	// to render generically — approval kinds carry different data and the UI
	// should not need a schema per kind.
	Payload  map[string]any    `json:"payload,omitempty"`
	Decision *ApprovalDecision `json:"decision,omitempty"`
}

// maxDecidedApprovals bounds the decided history the UI keeps.
const maxDecidedApprovals = 100

// recordApprovalRequest parks one approval request in the human's inbox.
func (s *Server) recordApprovalRequest(evt event.Event) {
	entry := &ApprovalEntry{
		EventID:   evt.ID,
		RequestID: evt.RequestId,
		WorkerID:  evt.WorkerId,
		TraceID:   evt.TraceID,
		Timestamp: evt.Timestamp,
		Payload:   evt.Payload,
	}
	if evt.Payload != nil {
		entry.Action, _ = evt.Payload["action"].(string)
		entry.Tool, _ = evt.Payload["tool"].(string)
		entry.Path, _ = evt.Payload["path"].(string)
	}
	s.approvalsMu.Lock()
	s.pendingApprovals = append(s.pendingApprovals, entry)
	s.approvalsMu.Unlock()
	log.Printf("[webui] approval requested by %s: %s %s", entry.WorkerID, entry.Tool, entry.Path)
}

// approvals returns the tracked entries: pending (oldest first) then the decided
// history (newest first).
func (s *Server) approvals() []ApprovalEntry {
	s.approvalsMu.Lock()
	defer s.approvalsMu.Unlock()
	out := make([]ApprovalEntry, 0, len(s.pendingApprovals)+len(s.decidedApprovals))
	for _, e := range s.pendingApprovals {
		out = append(out, *e)
	}
	for _, e := range s.decidedApprovals {
		out = append(out, *e)
	}
	return out
}

// sendApprovalDecision records the human's verdict and publishes approval.decision
// to the requester. The entry moves from pending to the decided history; an
// unknown request id is an error, because the UI polls a snapshot that may have
// moved on.
func (s *Server) sendApprovalDecision(ctx context.Context, requestID string, approved bool, note string) error {
	s.approvalsMu.Lock()
	idx := -1
	for i, e := range s.pendingApprovals {
		if e.RequestID == requestID {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.approvalsMu.Unlock()
		return fmt.Errorf("no pending approval with request id %s", requestID)
	}
	entry := s.pendingApprovals[idx]
	s.pendingApprovals = append(s.pendingApprovals[:idx], s.pendingApprovals[idx+1:]...)
	entry.Decision = &ApprovalDecision{Approved: approved, Note: note, Timestamp: time.Now().Unix()}
	s.decidedApprovals = append([]*ApprovalEntry{entry}, s.decidedApprovals...)
	if len(s.decidedApprovals) > maxDecidedApprovals {
		s.decidedApprovals = s.decidedApprovals[:maxDecidedApprovals]
	}
	s.approvalsMu.Unlock()

	h, err := s.busHandler()
	if err != nil {
		return err
	}
	evt := event.New(event.TypeApprovalDecision, h.ID(), map[string]any{
		"approved": approved,
		"note":     note,
	})
	evt.RequestId = entry.RequestID
	evt.TraceID = entry.TraceID
	if err := h.Send(ctx, evt, entry.WorkerID); err != nil {
		return fmt.Errorf("send decision to %s: %w", entry.WorkerID, err)
	}
	log.Printf("[webui] decision %v sent to %s (request %s)", approved, entry.WorkerID, entry.RequestID)
	return nil
}
