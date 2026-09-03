// Package sqlite implements [store.AppendStore] backed by a local SQLite database.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/store"

	_ "modernc.org/sqlite"
)

// Store is a persistent event store backed by SQLite.
type Store struct {
	db *sql.DB
}

// New opens (or creates) a SQLite-backed event store at the given path.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	// WAL mode for concurrent reads + writes.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: enable WAL: %w", err)
	}
	// busy_timeout makes SQLite block (up to N ms) when the write lock is
	// contended instead of immediately returning SQLITE_BUSY. Without it
	// (the default is 0) concurrent appends — multiple workers writing plus
	// history queries reading — fail instantly with "database is locked (5)",
	// and the event is dropped (delivered to SSE but never persisted, so the
	// event query silently misses tool replies). 5000 ms covers our 5 s Append
	// transaction timeout; WAL still serializes writers, but now they wait.
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: set busy_timeout: %w", err)
	}
	// synchronous=NORMAL keeps WAL durability while allowing group commits,
	// which pairs with the busy_timeout wait above to absorb write bursts.
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: set synchronous: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	query := `
	CREATE TABLE IF NOT EXISTS events (
		id              TEXT PRIMARY KEY,
		type            TEXT NOT NULL,
		worker_id       TEXT NOT NULL,
		payload         TEXT NOT NULL DEFAULT '{}',
		timestamp       INTEGER NOT NULL,
		target_worker_id TEXT DEFAULT '',
		recipients      TEXT DEFAULT '',
		trace_id        TEXT,
		request_id      TEXT,
		specversion     TEXT,
		dataschema      TEXT
	);
	CREATE INDEX IF NOT EXISTS idx_events_worker_time ON events(worker_id, timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_events_target_time ON events(target_worker_id, timestamp DESC);
	-- Bare timestamp index: serves chronological pagination of "all history"
	-- (no worker filter), which the worker-scoped indexes can't be used for at scale.
	CREATE INDEX IF NOT EXISTS idx_events_time ON events(timestamp DESC);
	`
	if _, err := s.db.Exec(query); err != nil {
		return err
	}
	// Older databases predate the request_id column: add it so request →
	// response pairing survives restart (the webui correlates a tool call with
	// its request.* result by request_id).
	rows, err := s.db.Query(`PRAGMA table_info(events)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasRequestID := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "request_id" {
			hasRequestID = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasRequestID {
		_, err = s.db.Exec(`ALTER TABLE events ADD COLUMN request_id TEXT`)
		return err
	}
	return nil
}

// Append implements [store.AppendStore].
func (s *Store) Append(ctx context.Context, events ...event.Event) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: append: %w", err)
	}
	defer tx.Rollback()

	for _, evt := range events {
		payload, _ := json.Marshal(evt.Payload)
		recipients, _ := json.Marshal(evt.Recipients)
		_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO events
			(id, type, worker_id, payload, timestamp, target_worker_id, recipients, trace_id, request_id, specversion, dataschema)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			evt.ID, evt.Type, evt.WorkerId, string(payload),
			evt.Timestamp, evt.TargetWorkerID, string(recipients), evt.TraceID, evt.RequestId, evt.SpecVersion, evt.DataSchema,
		)
		if err != nil {
			return fmt.Errorf("sqlite: append event %s: %w", evt.ID, err)
		}
	}
	return tx.Commit()
}

// ByWorker implements [store.EventStore].

// Replay implements [store.EventStore].

func (s *Store) List(ctx context.Context, workerID string, opts store.QueryOpts) ([]event.Event, error) {
	query := `SELECT id, type, worker_id, payload, timestamp, target_worker_id,
			recipients, trace_id, request_id, specversion, dataschema
			FROM events WHERE 1=1`
	var args []any

	if len(opts.WorkerIDs) > 0 {
		var conds []string
		for _, wid := range opts.WorkerIDs {
			conds = append(conds, "(worker_id = ? OR target_worker_id = ? OR recipients LIKE ?)")
			args = append(args, wid, wid, "%"+wid+"%")
		}
		query += " AND (" + strings.Join(conds, " OR ") + ")"
	} else if workerID != "*" && workerID != "" {
		query += " AND (worker_id = ? OR target_worker_id = ? OR recipients LIKE ?)"
		args = append(args, workerID, workerID, "%"+workerID+"%")
	}
	if opts.TraceID != "" {
		query += " AND trace_id = ?"
		args = append(args, opts.TraceID)
	}
	if opts.Since > 0 {
		query += " AND timestamp >= ?"
		args = append(args, opts.Since)
	}
	// Order and paginate by the implicit rowid (insertion order). Events are
	// appended as they occur, so rowid is the true chronological order and
	// deterministic — unlike the second-resolution timestamp (and even the
	// millisecond uuid time, since same-ms events still tie). Ordering by
	// timestamp alone made same-second events (e.g. thinking then response)
	// come back in a random/undefined order after restart, and timestamp-based
	// pagination silently skipped same-second events at page boundaries.
	if opts.AfterID != "" {
		query += " AND rowid > (SELECT rowid FROM events WHERE id = ?)"
		args = append(args, opts.AfterID)
	}
	if opts.BeforeID != "" {
		query += " AND rowid < (SELECT rowid FROM events WHERE id = ?)"
		args = append(args, opts.BeforeID)
	}
	if opts.Desc {
		query += " ORDER BY rowid DESC"
	} else {
		query += " ORDER BY rowid ASC"
	}
	if opts.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, opts.Limit)
	}

	return s.query(ctx, query, args...)
}

func (s *Store) query(ctx context.Context, query string, args ...any) ([]event.Event, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: query: %w", err)
	}
	defer rows.Close()

	var result []event.Event
	for rows.Next() {
		var (
			evt                 event.Event
			payloadRaw          string
			recipientsRaw       string
			traceID, rid, sv, ds sql.NullString
		)
		if err := rows.Scan(&evt.ID, &evt.Type, &evt.WorkerId, &payloadRaw,
			&evt.Timestamp, &evt.TargetWorkerID, &recipientsRaw, &traceID, &rid, &sv, &ds); err != nil {
			return nil, fmt.Errorf("sqlite: scan: %w", err)
		}
		if payloadRaw != "" {
			json.Unmarshal([]byte(payloadRaw), &evt.Payload)
		}
		if recipientsRaw != "" {
			json.Unmarshal([]byte(recipientsRaw), &evt.Recipients)
		}
		evt.TraceID = traceID.String
		evt.RequestId = rid.String
		evt.SpecVersion = sv.String
		evt.DataSchema = ds.String
		result = append(result, evt)
	}
	return result, rows.Err()
}
