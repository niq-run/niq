package directory

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/niq-run/niq/core/event"
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

func (s *stubChannel) result() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.sent) - 1; i >= 0; i-- {
		if s.sent[i].Type == event.TypeRequestCompleted {
			return s.sent[i].Payload["result"].(string)
		}
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

	invoke(w, TypeListWorkers, map[string]any{})
	var roster []map[string]any
	if err := json.Unmarshal([]byte(ch.result()), &roster); err != nil {
		t.Fatalf("list_workers not JSON array: %v", err)
	}
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
	// list must be SLIM: no tool schemas (those belong to get_worker_info).
	if _, hasTools := byID["workspace"]["tools"]; hasTools {
		t.Fatalf("list_workers must NOT carry tool details, got %v", roster)
	}

	// get_worker_info known → full record (tools included); unknown → fail.
	invoke(w, TypeGetWorkerInfo, map[string]any{"worker": "workspace"})
	var rec map[string]any
	if err := json.Unmarshal([]byte(ch.result()), &rec); err != nil {
		t.Fatalf("get_worker_info not JSON: %v", err)
	}
	if rec["type"] != "workspace" {
		t.Fatalf("get_worker_info type=%v", rec["type"])
	}
	if _, hasTools := rec["tools"]; !hasTools {
		t.Fatalf("get_worker_info must carry full tool detail, got %s", ch.result())
	}
	invoke(w, TypeGetWorkerInfo, map[string]any{"worker": "ghost"})
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

	invoke(w, TypeGetWorkerInfo, map[string]any{"worker": "niw-a"})
	var rec map[string]any
	if err := json.Unmarshal([]byte(ch.result()), &rec); err != nil {
		t.Fatalf("get_worker_info not JSON: %v", err)
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

	invoke(w, TypeListWorkers, map[string]any{})
	var roster []map[string]any
	_ = json.Unmarshal([]byte(ch.result()), &roster)
	if len(roster) != 1 || roster[0]["worker_id"] != "workspace" {
		t.Fatalf("after gone, roster should be just workspace: %s", ch.result())
	}
}
