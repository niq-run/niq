package history

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/store"
	"github.com/niq-run/niq/core/impl/eventbus"
)

// stubChannel satisfies corebus.WorkerSideChannel for handler tests. history
// handlers reply via Send, so we capture the emitted replies.
type stubChannel struct {
	mu   sync.Mutex
	sent []event.Event
}

func (s *stubChannel) ID() string { return "stub" }
func (s *stubChannel) Send(_ context.Context, evt event.Event, _ ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, evt)
	return nil
}
func (s *stubChannel) Broadcast(_ context.Context, _ event.Event) error { return nil }
func (s *stubChannel) Receive(_ context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *stubChannel) Close() error { return nil }

func (s *stubChannel) replies() []event.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]event.Event(nil), s.sent...)
}

func newTestWorker(t *testing.T, s store.EventStore, maxListEvents, maxListItemBytes int) (*Worker, *stubChannel) {
	t.Helper()
	ch := &stubChannel{}
	w := New(Config{ID: "history", Bus: ch, Store: s, MaxListEvents: maxListEvents, MaxListItemBytes: maxListItemBytes})
	w.registerTools() // what Start() does; the test drives handler dispatch directly
	return w, ch
}

// invoke dispatches a tool-call event of the given type/payload through the
// worker's extension machinery, as if a peer called the tool.
func invoke(w *Worker, typ event.EventType, payload map[string]any) {
	evt := event.New(typ, "caller", payload)
	evt.RequestId = "call-1"
	evt.TraceID = "trace-caller"
	w.DispatchExtension(evt)
}

// lastResult returns the raw "result" string of the most recent
// request.completed reply (history handlers reply once per invocation, so the
// latest reply corresponds to the latest invoke).
func lastResult(t *testing.T, ch *stubChannel) string {
	t.Helper()
	var last string
	for _, r := range ch.replies() {
		if r.Type == event.TypeRequestCompleted {
			last = r.Payload["result"].(string)
		}
	}
	if last == "" {
		t.Fatalf("no request.completed reply emitted")
	}
	return last
}

// listResult decodes a history.list reply into its result object.
func listResult(t *testing.T, ch *stubChannel) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(lastResult(t, ch)), &out); err != nil {
		t.Fatalf("history.list result not an object: %v", err)
	}
	return out
}

func TestHistoryListFiltersAndTruncates(t *testing.T) {
	s := eventbus.NewMemoryEventStore()
	events := []event.Event{
		{ID: "e1", Type: "bash", WorkerId: "a", TargetWorkerID: "workspace", Payload: map[string]any{"command": "ls"}, Timestamp: 100},
		{ID: "e2", Type: event.TypeRequestCompleted, WorkerId: "workspace", TargetWorkerID: "a", Payload: map[string]any{"result": "ok"}, Timestamp: 101},
		{ID: "e3", Type: "reason.thinking", WorkerId: "b", Payload: map[string]any{"content": string(make([]byte, 8000))}, Timestamp: 102},
	}
	if err := s.Append(context.Background(), events...); err != nil {
		t.Fatal(err)
	}
	w, ch := newTestWorker(t, s, 0, 200)

	// Filter to worker "a" on the sent side, type filter, ascending.
	invoke(w, TypeHistoryList, map[string]any{
		"worker": "a", "role": "sent", "type": "bash", "desc": false,
	})
	res := listResult(t, ch)
	list := res["events"].([]any)
	if len(list) != 1 {
		t.Fatalf("expected 1 event, got %d", len(list))
	}
	item := list[0].(map[string]any)
	if item["id"] != "e1" {
		t.Fatalf("expected e1, got %v", item["id"])
	}

	// The big payload must be truncated and flagged.
	invoke(w, TypeHistoryList, map[string]any{"worker": "b"})
	res = listResult(t, ch)
	item = res["events"].([]any)[0].(map[string]any)
	if item["payload_truncated"] != true {
		t.Fatalf("expected large payload to be flagged truncated")
	}
	if pl := item["payload"].(string); len(pl) > 300 { // 200 cap + marker
		t.Fatalf("truncated payload too long: %d bytes", len(pl))
	}
	if res["truncated"] != true {
		t.Fatalf("expected result.truncated=true")
	}
}

func TestHistoryListTypesFilterAndTruncationControl(t *testing.T) {
	s := eventbus.NewMemoryEventStore()
	events := []event.Event{
		{ID: "e-bash", Type: "bash", WorkerId: "a", Payload: map[string]any{"command": "ls"}, Timestamp: 100},
		{ID: "e-cc", Type: event.TypeRequestCompleted, WorkerId: "a", Payload: map[string]any{"result": "ok"}, Timestamp: 101},
		{ID: "e-think", Type: "reason.thinking", WorkerId: "a", Payload: map[string]any{"content": string(make([]byte, 300))}, Timestamp: 102},
	}
	if err := s.Append(context.Background(), events...); err != nil {
		t.Fatal(err)
	}
	w, ch := newTestWorker(t, s, 0, 200)

	// Multi-type filter: store-level, returns exactly the two requested types
	// for worker "a" on the sent side, in ascending order.
	invoke(w, TypeHistoryList, map[string]any{
		"worker": "a", "role": "sent", "types": []string{"bash", "reason.thinking"}, "desc": false,
	})
	res := listResult(t, ch)
	list := res["events"].([]any)
	if len(list) != 2 {
		t.Fatalf("expected 2 events, got %d", len(list))
	}
	if list[0].(map[string]any)["id"] != "e-bash" || list[1].(map[string]any)["id"] != "e-think" {
		t.Fatalf("unexpected order/results: %v %v", list[0].(map[string]any)["id"], list[1].(map[string]any)["id"])
	}

	// max_bytes overrides the worker default: cap the thinking payload at 10.
	invoke(w, TypeHistoryList, map[string]any{"worker": "a", "types": []string{"reason.thinking"}, "max_bytes": 10})
	res = listResult(t, ch)
	item := res["events"].([]any)[0].(map[string]any)
	if item["payload_truncated"] != true {
		t.Fatalf("expected truncation with max_bytes=10")
	}
	if pl := item["payload"].(string); len(pl) > 50 { // 10 cap + marker
		t.Fatalf("max_bytes=10 not respected: payload %d bytes", len(pl))
	}

	// truncate=false disables the cap entirely: full payload, no flag.
	invoke(w, TypeHistoryList, map[string]any{"worker": "a", "types": []string{"reason.thinking"}, "truncate": false})
	res = listResult(t, ch)
	item = res["events"].([]any)[0].(map[string]any)
	if item["payload_truncated"] != nil {
		t.Fatalf("truncate=false must not flag payloads")
	}
	if pl := item["payload"].(string); len(pl) < 200 {
		t.Fatalf("truncate=false should return the full payload; got %d bytes", len(pl))
	}
}

func TestHistoryListLimit(t *testing.T) {
	s := eventbus.NewMemoryEventStore()
	var events []event.Event
	for i := 0; i < 20; i++ {
		events = append(events, event.Event{
			ID: "e" + fmt.Sprint(i), Type: "x", WorkerId: "a", Timestamp: int64(100 + i),
		})
	}
	if err := s.Append(context.Background(), events...); err != nil {
		t.Fatal(err)
	}
	// Cap at 5 regardless of the package default of 50.
	w, ch := newTestWorker(t, s, 5, 200)
	invoke(w, TypeHistoryList, map[string]any{})
	res := listResult(t, ch)
	if n := int(res["count"].(float64)); n != 5 {
		t.Fatalf("expected limit 5, got %d", n)
	}
}

func TestHistoryGetByIDAndRequestID(t *testing.T) {
	s := eventbus.NewMemoryEventStore()
	events := []event.Event{
		{ID: "e1", Type: "bash", WorkerId: "a", TargetWorkerID: "workspace", Payload: map[string]any{"command": "ls"}, TraceID: "t", RequestId: "req-1", Timestamp: 100},
		{ID: "e2", Type: event.TypeRequestCompleted, WorkerId: "workspace", TargetWorkerID: "a", Payload: map[string]any{"result": "ok"}, TraceID: "t", RequestId: "req-1", Timestamp: 101},
	}
	if err := s.Append(context.Background(), events...); err != nil {
		t.Fatal(err)
	}
	w, ch := newTestWorker(t, s, 0, 0)

	// By id: full, untruncated payload survives as the real structured map.
	invoke(w, TypeHistoryGet, map[string]any{"id": "e2"})
	var byID []map[string]any
	if err := json.Unmarshal([]byte(lastResult(t, ch)), &byID); err != nil {
		t.Fatalf("history.get result not an array: %v", err)
	}
	ev := byID[0]
	if ev["id"] != "e2" {
		t.Fatalf("expected e2, got %v", ev["id"])
	}
	if _, ok := ev["payload"].(map[string]any); !ok {
		t.Fatalf("expected structured payload in history.get, got %T", ev["payload"])
	}

	// By request_id: returns the whole request→response pair.
	invoke(w, TypeHistoryGet, map[string]any{"request_id": "req-1"})
	var pair []map[string]any
	if err := json.Unmarshal([]byte(lastResult(t, ch)), &pair); err != nil {
		t.Fatalf("history.get result not an array: %v", err)
	}
	if n := len(pair); n != 2 {
		t.Fatalf("expected request→response pair (2 events), got %d", n)
	}
}

func TestHistoryGetUnknownFails(t *testing.T) {
	w, ch := newTestWorker(t, eventbus.NewMemoryEventStore(), 0, 0)
	invoke(w, TypeHistoryGet, map[string]any{"id": "nope"})
	for _, r := range ch.replies() {
		if r.Type == event.TypeRequestFailed {
			return
		}
	}
	t.Fatalf("expected request.failed for unknown id")
}
