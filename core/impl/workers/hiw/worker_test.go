package hiw

import (
	"context"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// stubChannel satisfies corebus.WorkerSideChannel for handler tests.
type stubChannel struct {
	sent       []event.Event
	targets    [][]string
	broadcasts []event.Event
}

func (s *stubChannel) ID() string { return "stub" }
func (s *stubChannel) Send(ctx context.Context, evt event.Event, targets ...string) error {
	s.sent = append(s.sent, evt)
	s.targets = append(s.targets, append([]string(nil), targets...))
	return nil
}
func (s *stubChannel) Broadcast(ctx context.Context, evt event.Event) error {
	s.broadcasts = append(s.broadcasts, evt)
	return nil
}
func (s *stubChannel) Receive(ctx context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *stubChannel) Close() error { return nil }

// TestApprovalRequestIsAdvertised verifies approval.request is a registered
// capability: it rides the worker.ready "watch" (so the directory roster and
// peer capability lookup can see the approval flow) and its handler parks the
// request without replying.
func TestApprovalRequestIsAdvertised(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	var found bool
	for _, e := range w.WatchEntries(false) {
		if e["event"] != string(event.TypeApprovalRequest) {
			continue
		}
		found = true
		params, ok := e["parameters"].(map[string]any)
		if !ok {
			t.Fatalf("approval.request advertised without a schema: %+v", e)
		}
		// The schema is an envelope, not a union of today's senders: only the
		// action label is fixed, everything else is requester-defined.
		if params["additionalProperties"] != true {
			t.Fatalf("approval.request schema is closed, so no action-specific context fits: %+v", params)
		}
		props, _ := params["properties"].(map[string]any)
		if len(props) != 1 {
			t.Fatalf("approval.request schema enumerates %d fields, want only action: %+v", len(props), props)
		}
		if _, ok := props["action"]; !ok {
			t.Fatalf("approval.request schema has no action field: %+v", props)
		}
	}
	if !found {
		t.Fatalf("approval.request is missing from the watch contract: %+v", w.WatchEntries(false))
	}

	req := approvalRequestEvent()
	if !w.DispatchExtension(req) {
		t.Fatal("approval.request is not dispatched")
	}
	if len(w.Approvals()) != 1 {
		t.Fatalf("approval request was not parked: %+v", w.Approvals())
	}
	if len(ch.sent) != 0 {
		t.Fatalf("approval.request must not be answered: %+v", ch.sent)
	}
}

// TestUnknownRequestIsAnswered verifies HIW behaves like every other worker for
// a request it does not serve: the caller gets request.failed instead of a
// silent drop.
func TestUnknownRequestIsAnswered(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	evt := event.New("not.served", "peer", nil)
	evt.RequestId = "call-7"
	if w.DispatchExtension(evt) {
		t.Fatal("an unserved request was dispatched")
	}
	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeRequestFailed {
		t.Fatalf("sent = %+v, want one request.failed", ch.sent)
	}
	if ch.sent[0].RequestId != "call-7" {
		t.Fatalf("reply request id = %q, want the caller's", ch.sent[0].RequestId)
	}
}

func approvalRequestEvent() event.Event {
	evt := event.New(event.TypeApprovalRequest, "ws-1", map[string]any{
		"worker_id": "reason-1",
		"action":    "mount.add",
		"tool":      "read",
		"path":      "/opt/tools",
	})
	evt.RequestId = evt.ID
	evt.TraceID = "trace-9"
	return evt
}

func TestApprovalLifecycle(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	// A pending request登记 → Approvals() exposes it undecided.
	req := approvalRequestEvent()
	w.recordApprovalRequest(req)
	entries := w.Approvals()
	if len(entries) != 1 || entries[0].Decision != nil {
		t.Fatalf("entries = %+v, want one pending", entries)
	}
	e := entries[0]
	if e.EventID != req.ID || e.RequestID != req.RequestId || e.WorkerID != "ws-1" ||
		e.Action != "mount.add" || e.Tool != "read" || e.Path != "/opt/tools" || e.TraceID != "trace-9" {
		t.Fatalf("entry = %+v", e)
	}

	// Decision: moves pending → decided and emits approval.decision to the
	// requester echoing the correlation fields.
	if err := w.SendApprovalDecision(context.Background(), req.RequestId, true, "ok"); err != nil {
		t.Fatalf("SendApprovalDecision: %v", err)
	}
	entries = w.Approvals()
	if len(entries) != 1 || entries[0].Decision == nil || !entries[0].Decision.Approved {
		t.Fatalf("entries after decision = %+v", entries)
	}
	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeApprovalDecision {
		t.Fatalf("sent = %+v, want one approval.decision", ch.sent)
	}
	dec := ch.sent[0]
	if dec.RequestId != req.RequestId || dec.TraceID != "trace-9" || dec.TargetWorkerID != "" {
		// TargetWorkerID is stamped by the bus; the send target is checked via
		// SendApprovalDecision's routing (verified by the successful Send).
		t.Fatalf("decision event = %+v", dec)
	}
	if dec.Payload["approved"] != true || dec.Payload["note"] != "ok" {
		t.Fatalf("decision payload = %+v", dec.Payload)
	}

	// A second decision on the same request errors — it is already decided.
	if err := w.SendApprovalDecision(context.Background(), req.RequestId, false, ""); err == nil {
		t.Fatalf("second decision must error")
	}
}

// TestSendEventCarriesRequestId verifies that a UI-driven extension call is a
// request: it carries its own ID as RequestId so the worker's reply pairs with
// it on the bus and in the UI.
func TestSendEventCarriesRequestId(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	reqID, err := w.SendEvent(context.Background(), "mount.list", "ws-1", nil)
	if err != nil {
		t.Fatalf("SendEvent: %v", err)
	}
	if len(ch.sent) != 1 {
		t.Fatalf("sent = %d events, want 1", len(ch.sent))
	}
	evt := ch.sent[0]
	if evt.RequestId == "" || evt.RequestId != evt.ID {
		t.Fatalf("RequestId = %q, must be the event's own id %q", evt.RequestId, evt.ID)
	}
	if reqID != evt.ID {
		t.Fatalf("SendEvent returned request id %q, want %q", reqID, evt.ID)
	}
	if evt.TraceID != evt.ID {
		t.Fatalf("TraceID = %q, want %q", evt.TraceID, evt.ID)
	}
}

func TestSendInputMultipleTargets(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	content := []map[string]any{{"type": "text", "text": "@a @b please sync"}}

	if err := w.SendInput(context.Background(), content, []string{"a", "b"}, "append"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	// One event, addressed to both workers: the bus copies it per target. The
	// recipients' own copies share the event id, so the UI pairs them up.
	if len(ch.sent) != 1 {
		t.Fatalf("sent %d events, want 1", len(ch.sent))
	}
	if got := ch.targets[0]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("targets = %v, want [a b]", got)
	}
	if len(ch.broadcasts) != 0 {
		t.Fatalf("broadcast %d times, want 0 for an addressed send", len(ch.broadcasts))
	}
	evt := ch.sent[0]
	if evt.Type != event.TypeWorkerInput || evt.WorkerId != "webui-hiw" {
		t.Fatalf("event = %+v", evt)
	}
	if mode := evt.Payload["input_mode"]; mode != "append" {
		t.Fatalf("input_mode = %v, want append", mode)
	}
}

func TestSendInputWithoutTargetsBroadcasts(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	if err := w.SendInput(context.Background(), []map[string]any{{"type": "text", "text": "hi"}}, nil, ""); err != nil {
		t.Fatalf("SendInput: %v", err)
	}
	if len(ch.sent) != 0 || len(ch.broadcasts) != 1 {
		t.Fatalf("sent=%d broadcasts=%d, want 0/1", len(ch.sent), len(ch.broadcasts))
	}
}

func TestSnapshotRestoreRoundtrip(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})
	w.recordApprovalRequest(approvalRequestEvent())
	w.recordApprovalRequest(approvalRequestEvent())
	if err := w.SendApprovalDecision(context.Background(), w.pendingApprovals[0].RequestID, false, "no"); err != nil {
		t.Fatal(err)
	}

	state, err := w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	w2 := New(Config{ID: "webui-hiw", Bus: ch})
	if err := w2.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	entries := w2.Approvals()
	var pending, decided int
	for _, e := range entries {
		if e.Decision == nil {
			pending++
		} else {
			decided++
		}
	}
	if pending != 1 || decided != 1 {
		t.Fatalf("after restore pending=%d decided=%d, want 1/1", pending, decided)
	}
}

// TestDiscoverIsAnswered verifies HIW answers a worker.discover with its
// contract, so the WebUI can learn what HIW serves.
//
// The WebUI drives the bus through HIW's identity, so the ask it sends about
// HIW arrives carrying HIW's OWN id — the case every other worker skips as a
// self-echo. HIW must answer it anyway: it is a genuine question about us.
func TestDiscoverIsAnswered(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	// The WebUI asks about HIW: a directed discover from HIW's identity.
	ask := event.New(event.TypeWorkerDiscover, "webui-hiw", nil)
	ask.TargetWorkerID = "webui-hiw"
	w.AnswerDiscover(ask, "hiw", hiwPublishes, false)

	if len(ch.sent) != 1 || ch.sent[0].Type != event.TypeWorkerReady {
		t.Fatalf("sent = %+v, want one worker.ready", ch.sent)
	}
	ready := ch.sent[0]
	if len(ch.targets) != 1 || ch.targets[0][0] != "webui-hiw" {
		t.Fatalf("reply targets = %+v, want it addressed to the asker", ch.targets)
	}
	watch, _ := ready.Payload["watch"].([]map[string]any)
	found := false
	for _, e := range watch {
		if e["event"] == string(event.TypeWorkerReadme) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the reply does not carry HIW's watch contract: %+v", watch)
	}
}

// TestOwnBroadcastDiscoverIsNotAnswered verifies HIW stays quiet about the
// discover it broadcasts at startup: the echo comes back to it, and it
// already knows its own contract.
func TestOwnBroadcastDiscoverIsNotAnswered(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "webui-hiw", Bus: ch})

	echo := event.New(event.TypeWorkerDiscover, "webui-hiw", nil)
	w.AnswerDiscover(echo, "hiw", hiwPublishes, false)

	if len(ch.sent) != 0 {
		t.Fatalf("our own discover echo was answered: %+v", ch.sent)
	}
}
