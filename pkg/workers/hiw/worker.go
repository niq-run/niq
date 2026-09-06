// Package hiw provides the Human Interface Worker — a minimal worker that
// publishes user input to the bus as the webui-hiw identity.
//
// HIW is the human's voice in the swarm. It does not manage UI lifecycle,
// stream events, or serve HTTP — those are the swarm's responsibility.
//
// It is also the default approver: workers configured with HIW as their
// approver send approval.request here, and HIW tracks the pending entries on
// the UI's behalf. The WebUI server only reads that state and sends the
// human's decision back through HIW — HIW owns the approval state, the server
// is a thin view over it.
package hiw

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/bus"
	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
)

// maxDecidedApprovals caps the decided-approval history kept for the UI.
const maxDecidedApprovals = 50

// ApprovalDecision is the human's verdict on one approval request.
type ApprovalDecision struct {
	Approved  bool   `json:"approved"`
	Note      string `json:"note,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// ApprovalEntry is one approval request HIW tracks on behalf of the UI.
type ApprovalEntry struct {
	EventID   string            `json:"id"`             // approval.request event id
	RequestID string            `json:"request_id"`     // correlation key (= its RequestId)
	WorkerID  string            `json:"worker_id"`      // requester (e.g. workspace worker)
	Action    string            `json:"action"`         // e.g. "mount.add"
	Tool      string            `json:"tool,omitempty"` // tool that raised the request
	Path      string            `json:"path,omitempty"` // boundary-expansion target
	TraceID   string            `json:"trace_id,omitempty"`
	Timestamp int64             `json:"timestamp"`
	// Payload is the approval request's full payload, carried through for the
	// UI to render generically — approval kinds may carry different data
	// (mounts context, tool arguments, ...) and the UI should not need a
	// schema per kind.
	Payload  map[string]any    `json:"payload,omitempty"`
	Decision *ApprovalDecision `json:"decision,omitempty"`
}

// Worker is the Human Interface Worker.
// It publishes worker.input events on behalf of the human user, and tracks
// the approval requests addressed to it.
type Worker struct {
	baseworker.BaseWorker
	started  bool
	cancelCh chan struct{}
	mu       sync.Mutex

	approvalsMu      sync.Mutex
	pendingApprovals []*ApprovalEntry // oldest first
	decidedApprovals []*ApprovalEntry // newest first, capped
}

// Config holds the configuration for a HIW.
type Config struct {
	ID  string // worker ID, defaults to "webui-hiw"
	Bus corebus.WorkerSideChannel
	// OnDurableChange is invoked when the approval state changed and must
	// survive a restart. Baseworker machinery; nil leaves signalling off.
	OnDurableChange func()
}

// New creates a new HIW worker.
func New(cfg Config) *Worker {
	id := cfg.ID
	if id == "" {
		id = "webui-hiw"
	}
	w := &Worker{
		BaseWorker: baseworker.NewBaseWorker(id, cfg.Bus),
		cancelCh:   make(chan struct{}),
	}
	w.SetOnDurableChange(cfg.OnDurableChange)
	return w
}

// Start starts the HIW worker's event loop.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return fmt.Errorf("hiw: already started")
	}

	busCh, _ := w.Channel.Receive(context.Background())

	// Announce HIW's presence on the bus.
	w.publishReady()
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), nil))

	// The durable-change signal (baseworker) delivers the persistence
	// callback off the event loop; no goroutine when nothing is installed.
	w.StartDurableLoop(ctx)

	ch := w.cancelCh
	go func() {
		for {
			select {
			case evt := <-busCh:
				// HIW reacts to nothing except the approval requests
				// addressed to it; everything else is drained (the swarm's
				// EventLog handles streaming).
				if evt.Type == event.TypeApprovalRequest {
					w.recordApprovalRequest(evt)
				}
			case <-ch:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	w.started = true
	return nil
}

// Stop stops the HIW worker.
func (w *Worker) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.started {
		return nil
	}
	close(w.cancelCh)
	w.started = false
	return nil
}

// ── Approval state ──

// recordApprovalRequest registers an approval.request addressed to HIW.
func (w *Worker) recordApprovalRequest(evt event.Event) {
	entry := &ApprovalEntry{
		EventID:   evt.ID,
		RequestID: evt.RequestId,
		WorkerID:  evt.WorkerId,
		Payload:   evt.Payload,
	}
	if evt.Payload != nil {
		entry.Action, _ = evt.Payload["action"].(string)
		entry.Tool, _ = evt.Payload["tool"].(string)
		entry.Path, _ = evt.Payload["path"].(string)
	}
	entry.TraceID = evt.TraceID
	entry.Timestamp = evt.Timestamp

	w.approvalsMu.Lock()
	w.pendingApprovals = append(w.pendingApprovals, entry)
	w.approvalsMu.Unlock()
	w.NotifyDurableChange()
	log.Printf("[hiw %s] approval requested by %s: %s %s", w.ID(), entry.WorkerID, entry.Tool, entry.Path)
}

// Approvals returns the tracked entries: pending (oldest first) plus the
// decided history (newest first).
func (w *Worker) Approvals() []ApprovalEntry {
	w.approvalsMu.Lock()
	defer w.approvalsMu.Unlock()
	out := make([]ApprovalEntry, 0, len(w.pendingApprovals)+len(w.decidedApprovals))
	for _, e := range w.pendingApprovals {
		out = append(out, *e)
	}
	for _, e := range w.decidedApprovals {
		out = append(out, *e)
	}
	return out
}

// SendApprovalDecision records the human's verdict for a pending entry and
// publishes approval.decision to the requesting worker. The entry moves from
// pending to the decided history. Unknown requestIDs are an error — the UI
// polls a snapshot that may have moved on.
func (w *Worker) SendApprovalDecision(ctx context.Context, requestID string, approved bool, note string) error {
	w.approvalsMu.Lock()
	idx := -1
	for i, e := range w.pendingApprovals {
		if e.RequestID == requestID {
			idx = i
			break
		}
	}
	if idx < 0 {
		w.approvalsMu.Unlock()
		return fmt.Errorf("no pending approval with request id %s", requestID)
	}
	entry := w.pendingApprovals[idx]
	w.pendingApprovals = append(w.pendingApprovals[:idx], w.pendingApprovals[idx+1:]...)
	entry.Decision = &ApprovalDecision{Approved: approved, Note: note, Timestamp: time.Now().Unix()}
	w.decidedApprovals = append([]*ApprovalEntry{entry}, w.decidedApprovals...)
	if len(w.decidedApprovals) > maxDecidedApprovals {
		w.decidedApprovals = w.decidedApprovals[:maxDecidedApprovals]
	}
	traceID := entry.TraceID
	target := entry.WorkerID
	w.approvalsMu.Unlock()
	w.NotifyDurableChange()

	evt := event.New(event.TypeApprovalDecision, w.ID(), map[string]any{
		"approved": approved,
		"note":     note,
	})
	evt.RequestId = requestID
	evt.TraceID = traceID
	if err := w.Channel.Send(ctx, evt, target); err != nil {
		return fmt.Errorf("send decision to %s: %w", target, err)
	}
	log.Printf("[hiw %s] decision %v sent to %s (request %s)", w.ID(), approved, target, requestID)
	return nil
}

// ── Persistence ──

// approvalsState is HIW's durable state: the tracked approval entries.
type approvalsState struct {
	Pending []*ApprovalEntry `json:"pending"`
	Decided []*ApprovalEntry `json:"decided"`
}

// Snapshot captures the approval state so pending requests survive a restart.
func (w *Worker) Snapshot() ([]byte, error) {
	w.approvalsMu.Lock()
	defer w.approvalsMu.Unlock()
	return json.Marshal(approvalsState{Pending: w.pendingApprovals, Decided: w.decidedApprovals})
}

// Restore rehydrates the approval state.
//
//	Called after construction and before Start.
func (w *Worker) Restore(state []byte) error {
	var s approvalsState
	if err := json.Unmarshal(state, &s); err != nil {
		return fmt.Errorf("hiw restore: %w", err)
	}
	w.approvalsMu.Lock()
	w.pendingApprovals = s.Pending
	w.decidedApprovals = s.Decided
	w.approvalsMu.Unlock()
	return nil
}

// ── Publishing ──

// SendInput publishes a user message to the bus as a worker.input event.
// If target is non-empty, the message is directed to that worker.
// mode controls input handling ("default", "schedule", "append").
func (w *Worker) SendInput(ctx context.Context, text string, target string, mode string) error {
	payload := map[string]any{"text": text}
	if mode != "" && mode != "default" {
		payload["input_mode"] = mode
	}
	evt := event.New(event.TypeWorkerInput, w.ID(), payload)
	evt.TraceID = evt.ID
	if target != "" {
		return w.Channel.Send(context.Background(), evt, target)
	}
	return w.Channel.Broadcast(context.Background(), evt)
}

// SendEvent publishes an arbitrary event on the bus as HIW — the human's
// voice sending a worker one of the events it declared it responds to (per its
// worker.ready "watch"). The event payload is the argument object (top level);
// the human UI uses this to drive a worker's behaviour or state. The event
// carries its own ID as RequestId: invoking a capability is a request, and the
// worker's reply echoes it back so the UI can pair the response with the card
// that raised it. The bus ACL still gates it: HIW may only publish event types
// its identity grants.
func (w *Worker) SendEvent(ctx context.Context, evtType event.EventType, target string, payload map[string]any) error {
	evt := event.New(evtType, w.ID(), payload)
	evt.TraceID = evt.ID
	evt.RequestId = evt.ID
	return w.Channel.Send(context.Background(), evt, target)
}

// publishReady announces HIW on the bus.
func (w *Worker) publishReady() {
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerReady, w.ID(), map[string]any{
		"worker_id": w.ID(),
		"type":      "hiw",
		"publishes": []map[string]any{
			{"type": "worker.input", "description": "User input event"},
			{"type": string(event.TypeApprovalDecision), "description": "Approval decision on a received approval.request"},
		},
	}))
}
