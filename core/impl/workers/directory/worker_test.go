package directory

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
)

// stubChannel satisfies corebus.WorkerSideChannel; it records replies.
type stubChannel struct {
	mu   sync.Mutex
	sent []event.Event
}

func (s *stubChannel) ID() string { return "stub" }
func (s *stubChannel) Send(_ context.Context, e event.Event, _ ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, e)
	return nil
}
func (s *stubChannel) Broadcast(_ context.Context, _ event.Event) error { return nil }
func (s *stubChannel) Receive(context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *stubChannel) Close() error { return nil }

// lastError returns the error text of the most recent request.failed reply.
func (s *stubChannel) lastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sent) - 1; i >= 0; i-- {
		if s.sent[i].Type != event.TypeRequestFailed {
			continue
		}
		msg, _ := s.sent[i].Payload["error"].(string)
		return msg
	}
	return ""
}

// result renders the last completed reply as JSON text. A structured reply
// carries its value directly (the worker no longer pre-encodes it), so both
// shapes are normalized here for assertions.
func (s *stubChannel) result() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sent) - 1; i >= 0; i-- {
		if s.sent[i].Type != event.TypeRequestCompleted {
			continue
		}
		// An object reply IS the payload; a string/array reply is wrapped in
		// "result". event.ReplyText normalizes both for assertions.
		return event.ReplyText(s.sent[i].Payload)
	}
	return ""
}

func (s *stubChannel) lastType() event.EventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return ""
	}
	return s.sent[len(s.sent)-1].Type
}

func invoke(w *DirectoryWorker, typ event.EventType, args map[string]any) {
	evt := event.New(typ, "caller", args)
	evt.RequestId = "call-1"
	w.DispatchExtension(evt)
}

func recall(w *DirectoryWorker, typ event.EventType, workerID string, payload map[string]any) {
	w.process(event.New(typ, workerID, payload))
}

// TestDirectoryCollectsFromBus verifies the roster is built from worker.ready
// on the BUS — including third-party/external workers the host never created
// and peer reason workers — not from anything directory spawned.
// TestReadmeText verifies worker.readme's guide names the worker's tools.
func TestReadmeText(t *testing.T) {
	w := New(Config{ID: "dir", Bus: &stubChannel{}})
	text := w.readmeText()
	for _, want := range []string{"search", "describe", "roster"} {
		if !strings.Contains(text, want) {
			t.Errorf("readme missing %q", want)
		}
	}
}

func TestDirectoryCollectsFromBus(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})

	recall(w, event.TypeWorkerReady, "workspace", map[string]any{
		"worker_id": "workspace", "type": "workspace",
		"watch": []map[string]any{{"event": "bash"}},
	})
	// A THIRD-PARTY worker connecting directly (not host-created).
	recall(w, event.TypeWorkerReady, "lark", map[string]any{
		"worker_id": "lark", "type": "lark",
		"watch": []map[string]any{{"event": "lark.send"}},
	})
	// A peer reason worker (present in roster, not callable as a tool).
	recall(w, event.TypeWorkerReady, "peer-reason", map[string]any{
		"worker_id": "peer-reason", "type": "reason",
		"watch": []map[string]any{{"event": "program.query"}},
	})

	invoke(w, TypeSearch, map[string]any{})
	var res struct {
		Total   int              `json:"total"`
		Workers []map[string]any `json:"workers"`
	}
	if err := json.Unmarshal([]byte(ch.result()), &res); err != nil {
		t.Fatalf("search not JSON object: %v", err)
	}
	roster := res.Workers
	if len(roster) != 3 {
		t.Fatalf("expected 3 (incl third-party lark + peer reason), got %d: %s", len(roster), ch.result())
	}
	byID := map[string]map[string]any{}
	for _, r := range roster {
		byID[r["worker_id"].(string)] = r
	}
	if byID["lark"]["type"] != "lark" || byID["peer-reason"]["type"] != "reason" {
		t.Fatalf("lark/peer-reason missing: %v", byID)
	}
	// list must be MINIMAL: identity only — no tool schemas, no published events
	// (both belong to describe).
	for _, r := range roster {
		if _, hasTools := r["tools"]; hasTools {
			t.Fatalf("search must NOT carry tool details, got %v", roster)
		}
		if _, hasPublishes := r["publishes"]; hasPublishes {
			t.Fatalf("search must NOT carry published events, got %v", roster)
		}
	}

	// describe known → full record (tools included); unknown → fail.
	invoke(w, TypeDescribe, map[string]any{"worker": "workspace"})
	var rec map[string]any
	if err := json.Unmarshal([]byte(ch.result()), &rec); err != nil {
		t.Fatalf("describe not JSON: %v", err)
	}
	if rec["type"] != "workspace" {
		t.Fatalf("describe type=%v", rec["type"])
	}
	if _, hasTools := rec["tools"]; !hasTools {
		t.Fatalf("describe must carry full tool detail, got %s", ch.result())
	}
	invoke(w, TypeDescribe, map[string]any{"worker": "ghost"})
	if ch.lastType() != event.TypeRequestFailed {
		t.Fatalf("unknown worker should fail")
	}
}

// TestDirectoryParsesRemoteReadyWatch verifies a remote worker's worker.ready —
// whose watch/publishes decode across HTTP to []any of maps, not []map[string]any —
// still populates the roster's tool list, so a third-party/remote worker is not
// listed with 0 tools.
func TestDirectoryParsesRemoteReadyWatch(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})
	w.process(event.New(event.TypeWorkerReady, "niw-a", map[string]any{
		"worker_id": "niw-a", "type": "niw",
		"watch": []any{map[string]any{"event": "niw.recipient.set"}, map[string]any{"event": "niw.recipient.unset"}},
	}))

	invoke(w, TypeDescribe, map[string]any{"worker": "niw-a"})
	var rec map[string]any
	if err := json.Unmarshal([]byte(ch.result()), &rec); err != nil {
		t.Fatalf("describe not JSON: %v", err)
	}
	tools, ok := rec["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("remote worker tools not parsed: %s", ch.result())
	}
}

// TestDirectoryForgetsOnGone verifies worker.gone removes a departed worker —
// including a third-party one.
func TestDirectoryForgetsOnGone(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})
	recall(w, event.TypeWorkerReady, "lark", map[string]any{"worker_id": "lark", "type": "lark"})
	recall(w, event.TypeWorkerReady, "workspace", map[string]any{"worker_id": "workspace", "type": "workspace"})
	recall(w, event.TypeWorkerGone, "lark", map[string]any{"worker_id": "lark"})

	invoke(w, TypeSearch, map[string]any{})
	var res struct {
		Workers []map[string]any `json:"workers"`
	}
	_ = json.Unmarshal([]byte(ch.result()), &res)
	roster := res.Workers
	if len(roster) != 1 || roster[0]["worker_id"] != "workspace" {
		t.Fatalf("after gone, roster should be just workspace: %s", ch.result())
	}
}

// searchIDs invokes search and returns the total plus the returned ids.
func searchIDs(t *testing.T, w *DirectoryWorker, ch *stubChannel, args map[string]any) (int, []string) {
	t.Helper()
	invoke(w, TypeSearch, args)
	if ch.lastType() != event.TypeRequestCompleted {
		t.Fatalf("search failed (%s): %s", ch.lastType(), ch.result())
	}
	var out struct {
		Total   int `json:"total"`
		Workers []struct {
			WorkerID string `json:"worker_id"`
		} `json:"workers"`
	}
	if err := json.Unmarshal([]byte(ch.result()), &out); err != nil {
		t.Fatalf("search result not JSON: %v (%s)", err, ch.result())
	}
	ids := make([]string, 0, len(out.Workers))
	for _, r := range out.Workers {
		ids = append(ids, r.WorkerID)
	}
	return out.Total, ids
}

// TestSearchWorkers verifies keyword search: AND across terms, case-insensitive
// matching over id/type/tool events and descriptions, id hits ranked first,
// limit, and an empty result set.
func TestSearchWorkers(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})

	recall(w, event.TypeWorkerReady, "test_mcp_fish", map[string]any{
		"type": "mcp",
		"watch": []map[string]any{
			{"event": "worker.readme", "desc": "Read this worker readme"},
			{"event": "mcp.tiny-fish.search", "desc": "Search the web for pages"},
			{"event": "mcp.tiny-fish.fetch_content", "desc": "Fetch a web page"},
			{"event": "mcp.tiny-fish.get_wallet", "desc": "Wallet balance"},
		},
	})
	recall(w, event.TypeWorkerReady, "oncall", map[string]any{
		"type": "reason",
		"watch": []map[string]any{
			{"event": "worker.readme", "desc": "Read this worker readme"},
			{"event": "send_message", "desc": "Send a message to a specific worker"},
		},
	})
	recall(w, event.TypeWorkerReady, "web-crawler", map[string]any{
		"type":  "reason",
		"watch": []map[string]any{{"event": "worker.readme"}},
	})

	// Case-insensitive tool-description match; the id hit ranks first.
	total, ids := searchIDs(t, w, ch, map[string]any{"query": "WEB"})
	if total != 2 || len(ids) != 2 || ids[0] != "web-crawler" || ids[1] != "test_mcp_fish" {
		t.Fatalf("query WEB → total=%d ids=%v, want web-crawler then test_mcp_fish", total, ids)
	}

	// AND: every term must match somewhere, so web-crawler drops out.
	total, ids = searchIDs(t, w, ch, map[string]any{"query": "web search"})
	if total != 1 || len(ids) != 1 || ids[0] != "test_mcp_fish" {
		t.Fatalf("query 'web search' → total=%d ids=%v, want only test_mcp_fish", total, ids)
	}

	// Id match ranks first even when another worker matches a description.
	total, ids = searchIDs(t, w, ch, map[string]any{"query": "fish"})
	if total != 1 || ids[0] != "test_mcp_fish" {
		t.Fatalf("query fish → total=%d ids=%v", total, ids)
	}

	// A tool name (not just its description) is searchable.
	total, ids = searchIDs(t, w, ch, map[string]any{"query": "get_wallet"})
	if total != 1 || ids[0] != "test_mcp_fish" {
		t.Fatalf("query get_wallet → total=%d ids=%v", total, ids)
	}

	// limit caps the returned list but not the reported total.
	total, ids = searchIDs(t, w, ch, map[string]any{"query": "web", "limit": float64(1)})
	if total != 2 || len(ids) != 1 {
		t.Fatalf("limit=1 → total=%d len=%d, want 2 and 1", total, len(ids))
	}

	// No match is an empty list, not an error.
	total, ids = searchIDs(t, w, ch, map[string]any{"query": "nope"})
	if total != 0 || len(ids) != 0 {
		t.Fatalf("query nope → total=%d ids=%v, want empty", total, ids)
	}

	// A missing query is the roster: every worker, no matched labels. This is
	// the list-half of the merged tool, not an error.
	total, ids = searchIDs(t, w, ch, map[string]any{})
	if total != 3 || len(ids) != 3 {
		t.Fatalf("roster → total=%d ids=%v, want all 3", total, ids)
	}

	// Type filters. exclude_types drops the other thinkers; types keeps only
	// them; an excluded type wins over an included one; both compose with a
	// query and apply to the roster too.
	total, ids = searchIDs(t, w, ch, map[string]any{"query": "web", "exclude_types": []string{"reason"}})
	if total != 1 || ids[0] != "test_mcp_fish" {
		t.Fatalf("exclude_types=reason → total=%d ids=%v, want only test_mcp_fish", total, ids)
	}
	total, _ = searchIDs(t, w, ch, map[string]any{"types": []string{"reason"}})
	if total != 2 {
		t.Fatalf("types=reason → total=%d, want 2", total)
	}
	total, _ = searchIDs(t, w, ch, map[string]any{"types": []string{"mcp"}, "exclude_types": []string{"MCP"}})
	if total != 0 {
		t.Fatalf("exclude must win over include → total=%d", total)
	}
}

// TestDirectoryListsAndDescribesItself verifies the directory is a member of its
// own roster: a caller asking "who is here" sees it, and describe returns its
// contract (type + the two tools it serves). Peers learn it from its ready
// broadcast, which excludes the sender, so this entry is added at start.
func TestDirectoryListsAndDescribesItself(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Stop() })

	invoke(w, TypeSearch, map[string]any{})
	var roster struct {
		Total   int `json:"total"`
		Workers []struct {
			WorkerID string `json:"worker_id"`
			Type     string `json:"type"`
		} `json:"workers"`
	}
	if err := json.Unmarshal([]byte(ch.result()), &roster); err != nil {
		t.Fatalf("search not JSON: %v", err)
	}
	if roster.Total != 1 || len(roster.Workers) != 1 {
		t.Fatalf("roster = %+v, want just this worker", roster)
	}
	if roster.Workers[0].WorkerID != "directory" || roster.Workers[0].Type != "directory" {
		t.Fatalf("roster entry = %+v, want the directory itself", roster.Workers[0])
	}

	invoke(w, TypeDescribe, map[string]any{"worker": "directory"})
	var rec struct {
		WorkerID string           `json:"worker_id"`
		Type     string           `json:"type"`
		Tools    []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(ch.result()), &rec); err != nil {
		t.Fatalf("describe not JSON: %v", err)
	}
	if rec.Type != "directory" {
		t.Fatalf("describe type = %q", rec.Type)
	}
	events := map[string]bool{}
	for _, tool := range rec.Tools {
		if e, ok := tool["event"].(string); ok {
			events[e] = true
		}
	}
	if !events["search"] || !events["describe"] {
		t.Fatalf("describe should list the directory's own tools: %v", events)
	}
	if events["worker.readme"] != true {
		t.Fatalf("the peer-facing contract should include worker.readme: %v", events)
	}
}

// TestDirectoryGoneOfSelfIsIgnored verifies a stray worker.gone naming the
// directory cannot remove it from its own roster.
func TestDirectoryGoneOfSelfIsIgnored(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = w.Stop() })

	recall(w, event.TypeWorkerGone, "directory", map[string]any{"worker_id": "directory"})

	invoke(w, TypeDescribe, map[string]any{"worker": "directory"})
	if ch.lastType() != event.TypeRequestCompleted {
		t.Fatalf("directory should still describe itself after a stray gone: %s", ch.lastType())
	}
}

// TestUnknownWorkerMessageNamesASend pins a real confusion: the old text said
// "call search", which reads as if search were a worker to address (and answers
// "target is not online"). The message must say who to send what.
func TestUnknownWorkerMessageNamesASend(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})

	invoke(w, TypeDescribe, map[string]any{"worker": "ghost"})
	if ch.lastType() != event.TypeRequestFailed {
		t.Fatalf("unknown worker should fail, got %s", ch.lastType())
	}
	msg := ch.lastError()
	if !strings.Contains(msg, "ghost") || !strings.Contains(msg, "search") {
		t.Fatalf("message should name the id and the search event: %q", msg)
	}
	if !strings.Contains(msg, `send "search" to "directory"`) {
		t.Fatalf("message should say what to send and to whom: %q", msg)
	}
	if strings.Contains(msg, "call search") {
		t.Fatalf("message must not phrase an event as a worker: %q", msg)
	}
}

// TestWorkerHelpServesGuides verifies the directory answers worker.help: a named
// event returns its long-form guide, and no event_name returns the index of
// everything it serves. Without this the prompt's "send worker.help for more
// detail" step would dead-end on the very worker every discovery starts at.
func TestWorkerHelpServesGuides(t *testing.T) {
	ch := &stubChannel{}
	w := New(Config{ID: "directory", Bus: ch})

	invoke(w, event.TypeWorkerHelp, map[string]any{"event_name": "search"})
	if got := ch.lastType(); got != event.TypeRequestCompleted {
		t.Fatalf("search guide: reply = %s, want request.completed (%s)", got, ch.lastError())
	}
	if guide := ch.result(); !strings.Contains(guide, "roster") || !strings.Contains(guide, "exclude_types") {
		t.Fatalf("search guide should explain both modes and the filters: %q", guide)
	}

	invoke(w, event.TypeWorkerHelp, map[string]any{"event_name": "describe"})
	if guide := ch.result(); !strings.Contains(guide, "parameters") {
		t.Fatalf("describe guide should explain the reply schema: %q", guide)
	}

	// No event_name: the index of everything this worker serves.
	invoke(w, event.TypeWorkerHelp, map[string]any{})
	if index := ch.result(); !strings.Contains(index, "search") || !strings.Contains(index, "describe") {
		t.Fatalf("help index should list both events: %q", index)
	}
}
