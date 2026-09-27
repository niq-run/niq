package timer

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/itfs/event"
)

// stubChannel satisfies corebus.WorkerSideChannel for handler tests. It is
// thread-safe because timers fire on their own goroutine while the test reads.
type stubChannel struct {
	mu   sync.Mutex
	sent []event.Event
}

func (s *stubChannel) ID() string { return "stub" }
func (s *stubChannel) Send(ctx context.Context, evt event.Event, targets ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, evt)
	return nil
}
func (s *stubChannel) Broadcast(ctx context.Context, evt event.Event) error { return nil }
func (s *stubChannel) Receive(ctx context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *stubChannel) Close() error { return nil }

// snapshot returns a copy of the events sent so far.
func (s *stubChannel) snapshot() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]event.Event, len(s.sent))
	copy(out, s.sent)
	return out
}

// waitSent blocks until at least n events have been sent or fails after 2s.
func waitSent(t *testing.T, ch *stubChannel, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(ch.snapshot()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d sent events; have %d", n, len(ch.snapshot()))
}

// firedEvent returns the first sent event of the given type.
func firedEvent(t *testing.T, ch *stubChannel, typ string) event.Event {
	t.Helper()
	for _, evt := range ch.snapshot() {
		if string(evt.Type) == typ {
			return evt
		}
	}
	t.Fatalf("no sent event of type %s; sent: %+v", typ, ch.snapshot())
	return event.Event{}
}

func resultMap(t *testing.T, evt event.Event) map[string]any {
	t.Helper()
	s, _ := evt.Payload["result"].(string)
	if s == "" {
		t.Fatalf("event %s has no result payload: %+v", evt.Type, evt.Payload)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("result not JSON: %s", s)
	}
	return m
}

func toolCall(typ event.EventType, callID, callerID string, args map[string]any) event.Event {
	evt := event.New(typ, callerID, args)
	evt.RequestId = callID
	evt.TraceID = "trace-x"
	return evt
}

func scheduleElapse(ch *stubChannel, w *Worker, callID string, ms int) {
	w.DispatchExtension(toolCall("elapse", callID, "reason-1", map[string]any{
		"duration_ms": float64(ms), "purpose": "ping",
	}))
}

// TestElapseFiresReminder verifies a relative timer publishes timer.reminder
// with the expected payload once it fires.
func TestElapseFiresReminder(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	scheduleElapse(ch, w, "e1", 30)

	waitSent(t, ch, 2) // request.completed reply + timer.reminder
	last := firedEvent(t, ch, "timer.reminder")
	if last.Payload["timer_id"] != "e1" || last.Payload["caller_id"] != "reason-1" {
		t.Fatalf("payload = %+v, want e1/reason-1 correlation", last.Payload)
	}
	res := resultMap(t, last)
	if res["tick_type"] != "reminder" || res["purpose"] != "ping" {
		t.Fatalf("result = %+v, want reminder/ping", res)
	}
}

// TestAbsoluteTimerAt verifies the "at" tool schedules a reminder from an
// RFC3339 fire_at and reports it via a timer.reminder event.
func TestAbsoluteTimerAt(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})

	fireAt := time.Now().UTC().Add(50 * time.Millisecond)
	if handled := w.DispatchExtension(toolCall("at", "a1", "reason-1", map[string]any{
		"fire_at": fireAt.Format(time.RFC3339), "purpose": "standup",
	})); !handled {
		t.Fatal("no handler for at")
	}

	waitSent(t, ch, 2)
	evt := firedEvent(t, ch, "timer.reminder")
	if evt.Payload["timer_id"] != "a1" {
		t.Fatalf("payload = %+v, want a1", evt.Payload)
	}
	res := resultMap(t, evt)
	if res["tick_type"] != "reminder" || res["purpose"] != "standup" {
		t.Fatalf("result = %+v, want reminder/standup", res)
	}
}

// TestAbsoluteUnixMS verifies the "at" tool also accepts unix_ms.
func TestAbsoluteUnixMS(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})

	fireMs := (time.Now().Add(40 * time.Millisecond)).UnixMilli()
	if handled := w.DispatchExtension(toolCall("at", "a2", "reason-1", map[string]any{
		"unix_ms": float64(fireMs), "purpose": "beat",
	})); !handled {
		t.Fatal("no handler for at")
	}

	waitSent(t, ch, 2)
	evt := firedEvent(t, ch, "timer.reminder")
	if evt.Payload["timer_id"] != "a2" {
		t.Fatalf("payload = %+v, want a2", evt.Payload)
	}
}

// TestAbsoluteBadFireAt verifies a malformed fire_at is rejected with a
// request.failed reply and no timer is scheduled.
func TestAbsoluteBadFireAt(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	if handled := w.DispatchExtension(toolCall("at", "a1", "reason-1", map[string]any{
		"fire_at": "not-a-time", "purpose": "x",
	})); !handled {
		t.Fatal("expected handler for at")
	}
	if len(ch.snapshot()) != 1 || ch.snapshot()[0].Type != event.TypeRequestFailed {
		t.Fatalf("expected request.failed, got %+v", ch.snapshot())
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, present := w.timers["a1"]; present {
		t.Fatal("rejected timer should not be scheduled")
	}
}

// TestSnapshotRestoreRoundtrip verifies pending timers survive a Snapshot →
// Restore cycle: the restored worker re-arms them so they still fire.
func TestSnapshotRestoreRoundtrip(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	scheduleElapse(ch, w, "e1", 80)
	scheduleElapse(ch, w, "e2", 200)

	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if blob == nil {
		t.Fatal("expected a non-nil snapshot with pending timers")
	}

	// Restore into a fresh worker (same way workerhost reconstructs it) with
	// its own channel, so its fires are isolated from the origin's replies.
	ch2 := &stubChannel{}
	fresh := New(Config{ID: "timer", Bus: ch2})
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	fresh.mu.Lock()
	if len(fresh.timers) != 2 {
		fresh.mu.Unlock()
		t.Fatalf("restored %d timers, want 2", len(fresh.timers))
	}
	fresh.mu.Unlock()

	// The restored timers each fire a timer.reminder (no replies on restore).
	waitSent(t, ch2, 2)
	for _, evt := range ch2.snapshot() {
		if string(evt.Type) != "timer.reminder" {
			t.Fatalf("evt type = %s, want timer.reminder", evt.Type)
		}
		if evt.Payload["timer_id"] != "e1" && evt.Payload["timer_id"] != "e2" {
			t.Fatalf("unexpected timer_id %v", evt.Payload["timer_id"])
		}
	}
}

// TestSnapshotEmptyRestoreNil verifies an empty worker snapshots to nil and a
// nil/empty Restore is a no-op.
func TestSnapshotEmptyRestoreNil(t *testing.T) {
	w := New(Config{ID: "timer", Bus: &stubChannel{}})
	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if blob != nil {
		t.Fatalf("empty snapshot = %s, want nil", blob)
	}
	if err := w.Restore(nil); err != nil {
		t.Fatalf("restore nil: %v", err)
	}
	if err := w.Restore([]byte{}); err != nil {
		t.Fatalf("restore empty: %v", err)
	}
}

// TestRestoreBadBlob verifies a malformed snapshot fails loudly.
func TestRestoreBadBlob(t *testing.T) {
	w := New(Config{ID: "timer", Bus: &stubChannel{}})
	if err := w.Restore([]byte("not json")); err == nil {
		t.Fatal("expected error restoring an invalid blob")
	}
}

// TestCancelAndDurableChange verifies the durable-change signal fires on
// schedule and on cancel, so the assembly layer persists each transition.
func TestCancelAndDurableChange(t *testing.T) {
	changes := make(chan struct{}, 8)
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch, OnDurableChange: func() { changes <- struct{}{} }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.StartDurableLoop(ctx)

	scheduleElapse(ch, w, "c1", 5000) // schedule → one durable change
	<-changes

	// Cancel → another durable change.
	if handled := w.DispatchExtension(toolCall("cancel", "cc", "reason-1", map[string]any{
		"timer_id": "c1",
	})); !handled {
		t.Fatal("no cancel handler")
	}
	<-changes

	// The cancelled timer is gone and never fires.
	w.mu.Lock()
	_, present := w.timers["c1"]
	w.mu.Unlock()
	if present {
		t.Fatal("cancelled timer still scheduled")
	}
}

// TestListPending verifies the list tool returns all pending timers with the
// expected fields.
func TestListPending(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	w.mu.Lock()
	w.timers["e1"] = &Entry{t: nil, spec: timerSpec{
		ID: "e1", CallerID: "reason-1", TickType: "reminder", Purpose: "ping",
		FireAt: time.Now().Add(time.Minute).UTC(), DurationMs: 60000,
	}}
	w.timers["a1"] = &Entry{t: nil, spec: timerSpec{
		ID: "a1", CallerID: "reason-1", TickType: "reminder", Purpose: "standup",
		FireAt: time.Now().Add(2 * time.Hour).UTC(), DurationMs: 7200000,
	}}
	w.mu.Unlock()

	if handled := w.DispatchExtension(toolCall("list", "li", "reason-1", nil)); !handled {
		t.Fatal("no handler for list")
	}
	sent := ch.snapshot()
	if len(sent) != 1 || sent[0].Type != event.TypeRequestCompleted {
		t.Fatalf("expected a request.completed reply, got %+v", sent)
	}
	res := resultMap(t, sent[0])
	raw, _ := res["timers"].([]any)
	if len(raw) != 2 {
		t.Fatalf("list returned %d timers, want 2: %s", len(raw), sent[0].Payload["result"])
	}
	ids := map[string]bool{}
	for _, it := range raw {
		m, _ := it.(map[string]any)
		ids[m["timer_id"].(string)] = true
		if m["caller_id"] != "reason-1" || m["tick_type"] != "reminder" {
			t.Fatalf("entry %+v", m)
		}
		if m["fire_at"] == "" {
			t.Fatalf("entry %+v missing fire_at", m)
		}
	}
	if !ids["e1"] || !ids["a1"] {
		t.Fatalf("list missing a scheduled timer: %v", ids)
	}
}

// TestListEmpty verifies list reports an empty set when nothing is scheduled.
func TestListEmpty(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	if handled := w.DispatchExtension(toolCall("list", "li", "reason-1", nil)); !handled {
		t.Fatal("no handler for list")
	}
	res := resultMap(t, ch.snapshot()[0])
	raw, _ := res["timers"].([]any)
	if len(raw) != 0 {
		t.Fatalf("expected empty list, got %+v", raw)
	}
}

// countType returns how many sent events are of the given type.
func countType(ch *stubChannel, typ string) int {
	n := 0
	for _, evt := range ch.snapshot() {
		if string(evt.Type) == typ {
			n++
		}
	}
	return n
}

// TestIntervalRepeats verifies an interval timer fires multiple timer.reminder
// events on its cadence and is still scheduled (recurring) afterwards.
func TestIntervalRepeats(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	if handled := w.DispatchExtension(toolCall("interval", "iv1", "reason-1", map[string]any{
		"interval_ms": float64(40), "purpose": "heartbeat",
	})); !handled {
		t.Fatal("no handler for interval")
	}

	// Wait for at least 3 reminder fires (plus the request.completed reply).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countType(ch, "timer.reminder") >= 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := countType(ch, "timer.reminder"); n < 3 {
		t.Fatalf("interval fired %d reminders, want >= 3", n)
	}

	// The recurring timer is still present (not dropped after firing).
	w.mu.Lock()
	e, present := w.timers["iv1"]
	w.mu.Unlock()
	if !present {
		t.Fatal("interval timer not present after firing")
	}
	if !e.spec.recurring() {
		t.Fatal("interval timer lost its recurring flag")
	}

	// The fired reminders carry the interval result.
	for _, evt := range ch.snapshot() {
		if string(evt.Type) != "timer.reminder" {
			continue
		}
		res := resultMap(t, evt)
		if res["tick_type"] != "interval" || res["interval_ms"] != float64(40) {
			t.Fatalf("interval result = %+v", res)
		}
	}
}

// TestIntervalBadIntervalMS verifies a non-positive interval_ms is rejected.
func TestIntervalBadIntervalMS(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	if handled := w.DispatchExtension(toolCall("interval", "iv0", "reason-1", map[string]any{
		"interval_ms": float64(0), "purpose": "x",
	})); !handled {
		t.Fatal("expected handler for interval")
	}
	sent := ch.snapshot()
	if len(sent) != 1 || sent[0].Type != event.TypeRequestFailed {
		t.Fatalf("expected request.failed, got %+v", sent)
	}
	w.mu.Lock()
	_, present := w.timers["iv0"]
	w.mu.Unlock()
	if present {
		t.Fatal("rejected interval timer should not be scheduled")
	}
}

// TestIntervalPersists verifies a recurring timer survives Snapshot → Restore
// and keeps its cadence: the restored worker both participates in list and
// keeps re-arming.
func TestIntervalPersists(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "timer", Bus: ch})
	if handled := w.DispatchExtension(toolCall("interval", "ivp", "reason-1", map[string]any{
		"interval_ms": float64(60), "purpose": "beat",
	})); !handled {
		t.Fatal("no handler for interval")
	}

	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if blob == nil {
		t.Fatal("expected non-nil snapshot with pending interval timer")
	}

	// Restoring into a fresh worker re-arms the repeated timer.
	ch2 := &stubChannel{}
	fresh := New(Config{ID: "timer", Bus: ch2})
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	fresh.mu.Lock()
	present := false
	for _, e := range fresh.timers {
		if e.spec.ID == "ivp" && e.spec.recurring() {
			present = true
		}
	}
	fresh.mu.Unlock()
	if !present {
		t.Fatal("restored worker lost the recurring interval timer")
	}

	// It keeps firing after restore.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if countType(ch2, "timer.reminder") >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := countType(ch2, "timer.reminder"); n < 2 {
		t.Fatalf("restored interval fired %d reminders, want >= 2", n)
	}
}
