package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"
	_ "modernc.org/sqlite"
)

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
