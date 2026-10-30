package history

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/impl/eventbus"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/store"
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
			last = event.ReplyText(r.Payload)
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

// TestReadmeText verifies worker.readme's guide names the worker's tools.
func TestReadmeText(t *testing.T) {
	w := New(Config{ID: "history", Bus: &stubChannel{}})
	text := w.readmeText()
	for _, want := range []string{"history.list", "history.get", "events/page"} {
		if !strings.Contains(text, want) {
			t.Errorf("readme missing %q", want)
		}
	}
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
	// types takes a single string as well as an array (one parameter, not two).
	invoke(w, TypeHistoryList, map[string]any{
		"worker": "a", "role": "sent", "types": "bash", "desc": false,
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

	// The big payload is OMITTED, not half-cut: an unparseable JSON fragment
	// would be useless to every caller. The preview is bounded, and the page-level
	// flag says some payload was left out.
	invoke(w, TypeHistoryList, map[string]any{"worker": "b"})
	res = listResult(t, ch)
	item = res["events"].([]any)[0].(map[string]any)
	if item["payload_omitted"] != true {
		t.Fatalf("expected large payload to be omitted")
	}
	if _, has := item["payload"]; has {
		t.Fatalf("an omitted payload must not be present: %v", item["payload"])
	}
	if pv, _ := item["payload_preview"].(string); len(pv) > 300 { // 200 cap + marker
		t.Fatalf("preview too long: %d bytes", len(pv))
	}
	if res["payload_omitted"] != true {
		t.Fatalf("expected page-level payload_omitted=true")
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

	// max_bytes is the per-item budget: over it the payload is omitted with a
	// bounded preview.
	invoke(w, TypeHistoryList, map[string]any{"worker": "a", "types": []string{"reason.thinking"}, "max_bytes": 10})
	res = listResult(t, ch)
	item := res["events"].([]any)[0].(map[string]any)
	if item["payload_omitted"] != true {
		t.Fatalf("expected the payload to be omitted with max_bytes=10")
	}
	if _, has := item["payload"]; has {
		t.Fatalf("omitted payload must not be present")
	}
	if pv, _ := item["payload_preview"].(string); len(pv) > 60 {
		t.Fatalf("max_bytes=10 not respected: preview %d bytes", len(pv))
	}

	// truncate=false ignores the budget: the payload comes back as the object.
	invoke(w, TypeHistoryList, map[string]any{"worker": "a", "types": []string{"reason.thinking"}, "truncate": false})
	res = listResult(t, ch)
	item = res["events"].([]any)[0].(map[string]any)
	if item["payload_omitted"] != nil {
		t.Fatalf("truncate=false must not flag payloads")
	}
	payload, ok := item["payload"].(map[string]any)
	if !ok {
		t.Fatalf("truncate=false should return the payload object, got %T", item["payload"])
	}
	if content, _ := payload["content"].(string); len(content) < 300 {
		t.Fatalf("truncate=false should return the full payload; got %d bytes", len(content))
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

// TestHistoryListPagination walks the whole history one page at a time in both
// directions, using the cursor the reply hands back, and checks the pages
// neither repeat nor skip events.
func TestHistoryListPagination(t *testing.T) {
	s := eventbus.NewMemoryEventStore()
	var events []event.Event
	for i := 0; i < 5; i++ {
		events = append(events, event.Event{
			ID: fmt.Sprintf("e%d", i), Type: "x", WorkerId: "a",
			Timestamp: int64(100 + i), Payload: map[string]any{"i": i},
		})
	}
	if err := s.Append(context.Background(), events...); err != nil {
		t.Fatal(err)
	}
	w, ch := newTestWorker(t, s, 2, 200) // page size 2

	walk := func(desc bool) (ids []string, cursors int) {
		cursor, param := "", ""
		for page := 0; page < 10; page++ {
			args := map[string]any{"worker": "a", "desc": desc}
			if cursor != "" {
				args[param] = cursor
			}
			invoke(w, TypeHistoryList, args)
			res := listResult(t, ch)
			list, _ := res["events"].([]any)
			if len(list) == 0 {
				break
			}
			for _, item := range list {
				ids = append(ids, item.(map[string]any)["id"].(string))
			}
			next, _ := res["next_cursor"].(string)
			nextParam, _ := res["next_cursor_param"].(string)
			if next == "" {
				break
			}
			cursors++
			cursor, param = next, nextParam
		}
		return ids, cursors
	}

	// Newest first: 2+2+1, walking backwards with "before".
	desc, cursors := walk(true)
	if got, want := strings.Join(desc, ","), "e4,e3,e2,e1,e0"; got != want {
		t.Fatalf("desc pages = %s, want %s", got, want)
	}
	if cursors != 2 { // the last page is short, so no cursor
		t.Fatalf("desc cursors = %d, want 2 full pages before the short one", cursors)
	}

	// Oldest first: the same walk forwards with "after".
	asc, cursors := walk(false)
	if got, want := strings.Join(asc, ","), "e0,e1,e2,e3,e4"; got != want {
		t.Fatalf("asc pages = %s, want %s", got, want)
	}
	if cursors != 2 {
		t.Fatalf("asc cursors = %d, want 2", cursors)
	}

	// A page that is not full carries no cursor at all (a bigger page size, so
	// all five events fit).
	big, bigCh := newTestWorker(t, s, 10, 200)
	invoke(big, TypeHistoryList, map[string]any{"worker": "a"})
	if cur, _ := listResult(t, bigCh)["next_cursor"].(string); cur != "" {
		t.Fatalf("short page should not offer a cursor, got %q", cur)
	}

	// Explicit anchors: a window between two ids.
	invoke(w, TypeHistoryList, map[string]any{"worker": "a", "after": "e1", "before": "e4", "desc": false})
	res := listResult(t, ch)
	var window []string
	for _, item := range res["events"].([]any) {
		window = append(window, item.(map[string]any)["id"].(string))
	}
	if got, want := strings.Join(window, ","), "e2,e3"; got != want {
		t.Fatalf("window (e1, e4) = %s, want %s", got, want)
	}
}

// TestHistoryListAndGetShareOneShape pins the fix for a caller-hostile
// inconsistency: history.list and history.get return the same record — the same
// field names, and payload as an OBJECT in both — so one piece of caller code
// reads either reply. Envelope names (worker_id / timestamp / ...) never leak
// out of get.
func TestHistoryListAndGetShareOneShape(t *testing.T) {
	s := eventbus.NewMemoryEventStore()
	evt := event.Event{
		ID: "e1", Type: "bash", WorkerId: "a", TargetWorkerID: "workspace",
		Payload: map[string]any{"command": "ls"}, Timestamp: 100,
		TraceID: "t1", RequestId: "r1", Recipients: []string{"x"},
	}
	if err := s.Append(context.Background(), evt); err != nil {
		t.Fatal(err)
	}
	w, ch := newTestWorker(t, s, 0, 200)

	invoke(w, TypeHistoryList, map[string]any{})
	fromList := listResult(t, ch)["events"].([]any)[0].(map[string]any)

	invoke(w, TypeHistoryGet, map[string]any{"id": "e1"})
	var fromGet []map[string]any
	if err := json.Unmarshal([]byte(lastResult(t, ch)), &fromGet); err != nil {
		t.Fatalf("history.get not an array: %v", err)
	}
	if len(fromGet) != 1 {
		t.Fatalf("history.get returned %d events", len(fromGet))
	}

	var listKeys, getKeys []string
	for k := range fromList {
		listKeys = append(listKeys, k)
	}
	for k := range fromGet[0] {
		getKeys = append(getKeys, k)
	}
	sort.Strings(listKeys)
	sort.Strings(getKeys)
	if strings.Join(listKeys, ",") != strings.Join(getKeys, ",") {
		t.Fatalf("field names differ — list: %v, get: %v", listKeys, getKeys)
	}

	for _, item := range []map[string]any{fromList, fromGet[0]} {
		if _, ok := item["payload"].(map[string]any); !ok {
			t.Fatalf("payload must be an object in both tools, got %T", item["payload"])
		}
	}

	for _, envelope := range []string{"worker_id", "target_worker_id", "timestamp", "specversion", "status"} {
		if _, ok := fromGet[0][envelope]; ok {
			t.Fatalf("history.get leaked the envelope field %q", envelope)
		}
	}
}
