package workspace

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/niq-run/niq/core/event"
	backend "github.com/niq-run/niq/pkg/services/wsbackend"
)

// stubChannel satisfies corebus.WorkerSideChannel for handler tests; it
// collects sent events so tests can assert on the reply.
type stubChannel struct {
	sent    []event.Event
	targets [][]string // per sent event: the directed targets
}

func (s *stubChannel) ID() string { return "stub" }
func (s *stubChannel) Send(ctx context.Context, evt event.Event, targets ...string) error {
	s.sent = append(s.sent, evt)
	s.targets = append(s.targets, targets)
	return nil
}
func (s *stubChannel) Broadcast(ctx context.Context, evt event.Event) error { return nil }
func (s *stubChannel) Receive(ctx context.Context) (<-chan event.Event, error) {
	ch := make(chan event.Event)
	return ch, nil
}
func (s *stubChannel) Close() error { return nil }

// lastReplyType returns the type of the most recent reply, or "".
func (s *stubChannel) lastReplyType() event.EventType {
	if len(s.sent) == 0 {
		return ""
	}
	return s.sent[len(s.sent)-1].Type
}

// TestSnapshotRestoreRoundtrip verifies that a runtime-modified mount set
// survives the Snapshot → Restore cycle (the persistence path behind
// mount.add + checkpoint).
func TestSnapshotRestoreRoundtrip(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()

	b := backend.NewEmbeddedBackend([]string{primary})
	w := New(Config{ID: "ws-test", Backend: b})

	if _, err := b.AddMount(extra); err != nil {
		t.Fatalf("AddMount: %v", err)
	}

	state, err := w.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var s workspaceState
	if err := json.Unmarshal(state, &s); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(s.Mounts) != 2 || s.Mounts[0] != primary || s.Mounts[1] != extra {
		t.Fatalf("snapshot mounts = %v", s.Mounts)
	}

	// A fresh backend built from config only (primary), then restored from
	// the snapshot: the runtime mount must come back.
	b2 := backend.NewEmbeddedBackend([]string{primary})
	w2 := New(Config{ID: "ws-test", Backend: b2})
	if err := w2.Restore(state); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	got := b2.Mounts()
	if len(got) != 2 || got[0] != primary || got[1] != extra {
		t.Fatalf("restored mounts = %v", got)
	}
}

// TestRestoreEmptySnapshotKeepsConfig verifies that field-absent snapshots
// leave config standing.
func TestRestoreEmptySnapshotKeepsConfig(t *testing.T) {
	primary := t.TempDir()
	b := backend.NewEmbeddedBackend([]string{primary})
	w := New(Config{ID: "ws-test", Backend: b})

	if err := w.Restore([]byte(`{}`)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := b.Mounts(); len(got) != 1 || got[0] != primary {
		t.Fatalf("mounts after empty restore = %v", got)
	}
}

// TestHandleMountAddRejectsEmpty verifies the handler's validation path: an
// empty path replies request.failed and changes nothing.
func TestHandleMountAddRejectsEmpty(t *testing.T) {
	primary := t.TempDir()
	b := backend.NewEmbeddedBackend([]string{primary})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-test", Bus: ch, Backend: b})

	evt := event.New(TypeMountAdd, "caller", map[string]any{})
	w.handleMountAdd(evt)
	if ch.lastReplyType() != event.TypeRequestFailed {
		t.Fatalf("reply = %v, want request.failed", ch.lastReplyType())
	}
	if got := b.Mounts(); len(got) != 1 {
		t.Fatalf("mounts after failed add = %v", got)
	}
}

// TestHandleMountAddAcceptsNonexistent verifies the lenient semantics: a
// not-yet-existing path is mounted as a phantom entry — approval grants
// scope, not existence, and the directory may be created later.
func TestHandleMountAddAcceptsNonexistent(t *testing.T) {
	primary := t.TempDir()
	b := backend.NewEmbeddedBackend([]string{primary})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-test", Bus: ch, Backend: b})

	missing := filepath.Join(primary, "future")
	w.handleMountAdd(event.New(TypeMountAdd, "caller", map[string]any{"path": missing}))
	if ch.lastReplyType() != event.TypeRequestCompleted {
		t.Fatalf("reply = %v, want completed", ch.lastReplyType())
	}
	if !mountsContain(b.Mounts(), missing) {
		t.Fatalf("mounts = %v, want %s", b.Mounts(), missing)
	}
}

// TestHandleMountAddAndList verifies the success path end to end: a valid
// mount.add appends the mount, replies request.completed, and mount.list
// reflects it.
func TestHandleMountAddAndList(t *testing.T) {
	primary := t.TempDir()
	extra := t.TempDir()
	b := backend.NewEmbeddedBackend([]string{primary})
	ch := &stubChannel{}
	w := New(Config{ID: "ws-test", Bus: ch, Backend: b})

	evt := event.New(TypeMountAdd, "caller", map[string]any{"path": extra})
	w.handleMountAdd(evt)
	if ch.lastReplyType() != event.TypeRequestCompleted {
		t.Fatalf("reply = %v, want request.completed", ch.lastReplyType())
	}
	var addReply struct {
		Added  string   `json:"added"`
		Mounts []string `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(ch.sent[len(ch.sent)-1].Payload["result"].(string)), &addReply); err != nil {
		t.Fatalf("parse add reply: %v", err)
	}
	if addReply.Added != extra || len(addReply.Mounts) != 2 {
		t.Fatalf("add reply = %+v", addReply)
	}

	w.handleMountList(event.New(TypeMountList, "caller", map[string]any{}))
	if ch.lastReplyType() != event.TypeRequestCompleted {
		t.Fatalf("list reply = %v, want request.completed", ch.lastReplyType())
	}
	var listReply struct {
		Mounts []string `json:"mounts"`
	}
	if err := json.Unmarshal([]byte(ch.sent[len(ch.sent)-1].Payload["result"].(string)), &listReply); err != nil {
		t.Fatalf("parse list reply: %v", err)
	}
	if len(listReply.Mounts) != 2 || listReply.Mounts[0] != primary || listReply.Mounts[1] != extra {
		t.Fatalf("list reply mounts = %v", listReply.Mounts)
	}
}
