package api

import (
	"context"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/impl/iface"
	"github.com/niq-run/niq/core/itfs/event"
)

// This file is the api package's iface.Backend: the half of *Server that faces
// the bus, next to server.go's half that faces the human. It is named for the
// interface it implements, not for a server-side "backend" — there is no other
// backend in this package.
//
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

// inbox returns the interface worker's approval inbox, if it serves approvals.
// The mechanism (parking, listing, publishing the decision, declaring the
// capability in the worker's watch contract) lives in iface, because every
// outward surface needs it; the app only renders and answers it.
func (s *Server) inbox() (iface.Inbox, error) {
	h, err := s.busHandler()
	if err != nil {
		return nil, err
	}
	in, ok := h.(iface.Inbox)
	if !ok {
		return nil, fmt.Errorf("this worker does not serve approvals")
	}
	return in, nil
}

// approvals returns the inbox contents for the UI: pending (oldest first) then
// the decided history (newest first), reshaped for the endpoint.
func (s *Server) approvals() []ApprovalEntry {
	in, err := s.inbox()
	if err != nil {
		return nil
	}
	out := make([]ApprovalEntry, 0, 8)
	for _, e := range in.Pending() {
		out = append(out, ApprovalEntry(e))
	}
	for _, e := range in.Decided() {
		out = append(out, ApprovalEntry(e))
	}
	return out
}

// ApprovalEntry is the interface worker's inbox entry, re-exported so the HTTP
// layer keeps talking in webui types.
type ApprovalEntry = iface.ApprovalEntry

// ApprovalDecision mirrors the interface worker's verdict.
type ApprovalDecision = iface.ApprovalDecision

// sendApprovalDecision answers a parked request.
func (s *Server) sendApprovalDecision(ctx context.Context, requestID string, approved bool, note string) error {
	in, err := s.inbox()
	if err != nil {
		return err
	}
	return in.Decide(ctx, requestID, approved, note)
}
