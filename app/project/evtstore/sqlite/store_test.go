package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/store"
	_ "modernc.org/sqlite"
)

// TestListTypeBlacklist verifies the exact + prefix type blacklist with a keep
// override — the talk view's "invisible event" filter moved server-side so
// history pages count real conversation rows instead of worker.* lifecycle
// noise and delta partials.
func TestListTypeBlacklist(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	events := []event.Event{
		event.New("worker.input", "hiw", map[string]any{"text": "hi"}),
		event.New("worker.abort", "hiw", map[string]any{}),
		event.New("worker.updated", "niq", map[string]any{}),
		event.New("reason.thinking_delta", "niq", map[string]any{}),
		event.New("reason.response", "niq", map[string]any{}),
	}
	if err := s.Append(ctx, events...); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// worker.* is hidden except input/abort; the delta partial is hidden for
	// good measure.
	got, err := s.List(ctx, "*", store.QueryOpts{
		ExcludeTypes:        []string{"reason.thinking_delta"},
		ExcludeTypePrefixes: []string{"worker."},
		KeepTypes:           []string{"worker.input", "worker.abort"},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var types []string
	for _, e := range got {
		types = append(types, string(e.Type))
	}
	slices.Sort(types)
	want := []string{"reason.response", "worker.abort", "worker.input"}
	if !slices.Equal(types, want) {
		t.Fatalf("blacklist retained %v, want %v", types, want)
	}
}

// TestRequestIDRoundtrip verifies the request_id pairing field survives a
// write + read cycle. The webui correlates a tool invocation with its
// request.* result by request_id, so losing it on persistence would hide
// results after a restart.
func TestRequestIDRoundtrip(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	evt := event.New("hello.greet", "niq", map[string]any{"arguments": map[string]any{}})
	evt.RequestId = "call-123"
	evt.TraceID = "trace-1"
	evt.TargetWorkerID = "hello"

	if err := s.Append(context.Background(), evt); err != nil {
		t.Fatalf("Append: %v", err)
	}

	list, err := s.List(context.Background(), "*", store.QueryOpts{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List returned %d events, want 1", len(list))
	}
	if got := list[0].RequestId; got != "call-123" {
		t.Fatalf("request_id = %q, want call-123", got)
	}

	// Get: point lookup by id returns the full stored event.
	got, found, err := s.Get(context.Background(), evt.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || got.RequestId != "call-123" || got.TargetWorkerID != "hello" {
		t.Fatalf("Get = %+v, found=%v", got, found)
	}
	if _, found, _ := s.Get(context.Background(), "unknown-id"); found {
		t.Fatalf("Get of unknown id must report found=false")
	}
}

// TestListRequestIDFilter verifies the request_id query option pairs an
// invocation event with its request.* reply under SQL, mirroring the webui's
// "find the response for this request" flow.
func TestListRequestIDFilter(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	inv := event.New("bash", "niq", map[string]any{"cmd": "ls"})
	inv.RequestId = "req-1"
	reply := event.New(event.TypeRequestCompleted, "ws", map[string]any{"result": "ok"})
	reply.RequestId = "req-1"
	other := event.New("bash", "niq", map[string]any{"cmd": "pwd"})
	other.RequestId = "req-2"

	if err := s.Append(context.Background(), inv, reply, other); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.List(context.Background(), "*", store.QueryOpts{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("request filter = %d events, want 2 (invocation + reply)", len(got))
	}
	for _, e := range got {
		if e.RequestId != "req-1" {
			t.Fatalf("event %s has request_id %q, want req-1", e.ID, e.RequestId)
		}
	}
}

// TestMigrateAddsRequestIDColumn verifies a database created with the old
// schema (no request_id) is upgraded in place by New's migrate step, and that
// newly appended events persist request_id.
func TestMigrateAddsRequestIDColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")

	// Create a db with the pre-request_id schema.
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
		CREATE TABLE events (
			id              TEXT PRIMARY KEY,
			type            TEXT NOT NULL,
			worker_id       TEXT NOT NULL,
			payload         TEXT NOT NULL DEFAULT '{}',
			timestamp       INTEGER NOT NULL,
			target_worker_id TEXT DEFAULT '',
			recipients      TEXT DEFAULT '',
			trace_id        TEXT,
			specversion     TEXT,
			dataschema      TEXT
		)`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := New(path)
	if err != nil {
		t.Fatalf("New (migrate): %v", err)
	}
	defer s.Close()

	evt := event.New("hello.greet", "hello", nil)
	evt.RequestId = "call-456"
	if err := s.Append(context.Background(), evt); err != nil {
		t.Fatalf("Append after migrate: %v", err)
	}
	list, err := s.List(context.Background(), "*", store.QueryOpts{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].RequestId != "call-456" {
		t.Fatalf("after migrate got %+v, want 1 event with request_id call-456", list)
	}
}
