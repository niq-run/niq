package reason

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/core/llm"
	"github.com/niq-run/niq/core/worker"
)

// ── mock bus channel ────────────────────────────────────────────────────────

// mockChannel is a minimal WorkerSideChannel for tests: it delivers events the
// test feeds in, and records everything the worker publishes/sends.
type mockChannel struct {
	in  chan event.Event
	mu  sync.Mutex
	out []event.Event
}

func newMockChannel() *mockChannel { return &mockChannel{in: make(chan event.Event, 16)} }

func (m *mockChannel) ID() string { return "mock" }
func (m *mockChannel) Send(_ context.Context, evt event.Event, _ ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, evt)
	return nil
}
func (m *mockChannel) Broadcast(_ context.Context, evt event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, evt)
	return nil
}
func (m *mockChannel) Receive(_ context.Context) (<-chan event.Event, error) { return m.in, nil }
func (m *mockChannel) Close() error                                          { return nil }

// eventsOf returns all recorded events of the given type.
func (m *mockChannel) eventsOf(typ event.EventType) []event.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []event.Event
	for _, e := range m.out {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

func (m *mockChannel) hasInterrupted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.out {
		if e.Type == "reason.end" {
			if sr, _ := e.Payload["stop_reason"].(string); sr == "interrupted" {
				return true
			}
		}
	}
	return false
}

func (m *mockChannel) hasErrorResponse() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.out {
		if e.Type == "reason.response" {
			return true
		}
	}
	return false
}

// ── LLM providers ───────────────────────────────────────────────────────────

// staticProvider returns a fixed completion immediately. Used for tests that
// just need a deterministic reasoning round to complete.
type staticProvider struct {
	msg llm.Message
}

func (p *staticProvider) Complete(context.Context, *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	return &llm.CompletionResponse{Message: p.msg}, nil
}
func (p *staticProvider) CompleteStream(_ context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	es := llm.NewEventStream()
	es.Push(llm.EventTextStart{})
	es.Push(llm.EventTextEnd{})
	es.End(p.msg)
	return es, nil
}
func (p *staticProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// blockingProvider blocks CompleteStream until its ctx is cancelled or release
// is closed, letting a test hold a reasoning round in flight.
type blockingProvider struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingProvider) Complete(ctx context.Context, _ *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	p.once.Do(func() { close(p.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
	}
	return &llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, StopReason: "stop"}}, nil
}
func (p *blockingProvider) CompleteStream(ctx context.Context, _ *llm.CompletionRequest) (*llm.EventStream, error) {
	p.once.Do(func() { close(p.started) })
	es := llm.NewEventStream()
	go func() {
		select {
		case <-ctx.Done():
			es.Abort(ctx.Err())
		case <-p.release:
			es.Push(llm.EventTextStart{})
			es.Push(llm.EventTextDelta{Delta: "hi"})
			es.Push(llm.EventTextEnd{})
			es.End(llm.Message{Role: llm.RoleAssistant, StopReason: "stop",
				Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}})
		}
	}()
	return es, nil
}
func (p *blockingProvider) ListModels(context.Context) ([]llm.ModelInfo, error) { return nil, nil }

// ── helpers ─────────────────────────────────────────────────────────────────

func waitCond(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", msg)
}

func startWorker(t *testing.T, prov llm.LLMProvider) (*Worker, *mockChannel, context.CancelFunc) {
	t.Helper()
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: prov, Bus: ch})
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		w.Stop()
		cancel()
	})
	return w, ch, cancel
}

// ── NewWorker assembly & lifecycle ──────────────────────────────────────────

// TestNewWorkerDefaults verifies a worker built with a minimal Config gets a
// sensible ID and the built-in subscriptions.
func TestNewWorkerDefaults(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if w.ID() != "r1" {
		t.Fatalf("ID = %q, want r1", w.ID())
	}
	// Built-in subscriptions (tool lifecycle, worker presence, timer) must be present.
	subs := w.Subscriptions()
	got := map[string]bool{}
	for _, s := range subs {
		got[string(s.Type)] = true
	}
	for _, want := range []string{"request.completed", "request.failed", "request.rejected",
		"send_message", "list_workers", "context.compress", "context.rotate", "provider.switch",
		"worker.ready", "worker.gone", "worker.discover", "worker.input", "worker.abort",
		"timer.timeout", "timer.reminder"} {
		if !got[want] {
			t.Errorf("missing built-in subscription %q", want)
		}
	}
}

// TestStartStop verifies Start/Stop lifecycle: Start is idempotent-guarded and
// Stop returns cleanly.
func TestStartStop(t *testing.T) {
	ch := newMockChannel()
	w := NewWorker(Config{ID: "r1", Provider: &staticProvider{}, Bus: ch})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := w.Start(ctx); err == nil {
		t.Fatal("second Start should fail (already started)")
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := w.Stop(); err != nil {
		t.Fatalf("second Stop should be a no-op, got %v", err)
	}
}

// TestWorkerImplementsManagedWorker asserts the reason worker satisfies the
// core ManagedWorker contract.
func TestWorkerImplementsManagedWorker(t *testing.T) {
	var _ worker.ManagedWorker = NewWorker(Config{ID: "r1", Bus: newMockChannel()})
}

// ── Snapshot / Restore (durable state) ──────────────────────────────────────

// TestSnapshotRestoreRoundTrip verifies Snapshot captures the reasoning
// transcript and Restore rehydrates it into a fresh worker — the durable state
// that must survive a suspend/resume or crash recovery.
func TestSnapshotRestoreRoundTrip(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})

	// Seed a transcript: a user message, an assistant response, and a tool result,
	// by restoring it from an accumulated transcript blob (the transcript's own
	// write path). This also exercises Restore as the seed mechanism.
	seed := []llm.Message{
		{Role: llm.RoleUser, Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hello"}}},
		{Role: llm.RoleAssistant, StopReason: "stop", Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "hi"}}},
		{Role: llm.RoleToolResult, ToolCallID: "call_1", ToolName: "workspace.bash", IsError: false,
			Content: []llm.ContentBlock{{Type: llm.ContentText, Text: "ok"}}},
	}
	// Restore expects a worker snapshot, whose transcript is nested alongside
	// the worker's provider selection — so seed a worker through its own
	// construction path and snapshot that, rather than handing over a bare
	// transcript blob.
	seeded := NewWorker(Config{ID: "r1", Bus: newMockChannel(), SeedMessages: seed})
	blob0, err := seeded.Snapshot()
	if err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	if err := w.Restore(blob0); err != nil {
		t.Fatalf("seed restore: %v", err)
	}

	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(blob) == 0 {
		t.Fatal("snapshot blob is empty")
	}

	// Restore into a fresh worker, as happens on suspend/resume or restart.
	fresh := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}

	gotMsgs, wantMsgs := fresh.Messages(), w.Messages()
	if len(gotMsgs) != len(wantMsgs) {
		t.Fatalf("restored %d messages, want %d", len(gotMsgs), len(wantMsgs))
	}
	for i := range wantMsgs {
		got, want := gotMsgs[i], wantMsgs[i]
		if got.Role != want.Role || got.StopReason != want.StopReason ||
			got.ToolCallID != want.ToolCallID || got.ToolName != want.ToolName ||
			got.IsError != want.IsError {
			t.Fatalf("message %d mismatch after round-trip:\n got %+v\nwant %+v", i, got, want)
		}
		if len(got.Content) != len(want.Content) {
			t.Fatalf("message %d content len mismatch", i)
		}
		for j := range want.Content {
			if got.Content[j].Type != want.Content[j].Type ||
				got.Content[j].Text != want.Content[j].Text {
				t.Fatalf("message %d content[%d] mismatch after round-trip", i, j)
			}
		}
	}
}

// TestSnapshotRestoreBadBlob verifies Restore rejects an invalid blob.
func TestSnapshotRestoreBadBlob(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if err := w.Restore([]byte("not json")); err == nil {
		t.Fatal("expected error restoring an invalid blob")
	}
}

// TestSnapshotEmptyMessages verifies a fresh worker snapshots to a valid,
// restorable empty-transcript blob.
func TestSnapshotEmptyMessages(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	blob, err := w.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	fresh := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if err := fresh.Restore(blob); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(fresh.Messages()) != 0 {
		t.Fatalf("restored %d messages, want 0", len(fresh.Messages()))
	}
}

// ── Async reasoning (reason() runs on its own goroutine) ────────────────────

// TestAsyncAbortInterruptsReasoning verifies reason() runs on its own
// goroutine: while an LLM call is in flight, the watch loop still processes
// events (an abort), and the abort's cancelReason interrupts the blocked call
// WITHOUT the test having to release the provider.
func TestAsyncAbortInterruptsReasoning(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start (LLM call in flight)")

	// Feed an abort while reasoning is blocked. Because reason() is async, the
	// watch loop processes it immediately and cancelReason interrupts the call.
	ch.in <- event.New(event.TypeWorkerAbort, "swarm", map[string]any{})
	waitCond(t, 2*time.Second, ch.hasInterrupted, "reason.end(interrupted)")
}

// TestAsyncNoFakeErrorOnInterrupt ensures cancellation does not publish a
// spurious "Error: context canceled" response.
func TestAsyncNoFakeErrorOnInterrupt(t *testing.T) {
	prov := &blockingProvider{started: make(chan struct{}), release: make(chan struct{})}
	_, ch, _ := startWorker(t, prov)

	ch.in <- event.New(event.TypeWorkerInput, "hiw", map[string]any{"text": "hello", "input_mode": "default"})
	waitCond(t, 2*time.Second, func() bool {
		select {
		case <-prov.started:
			return true
		default:
			return false
		}
	}, "reasoning to start")
	ch.in <- event.New(event.TypeWorkerAbort, "swarm", map[string]any{})
	waitCond(t, 2*time.Second, ch.hasInterrupted, "reason.end(interrupted)")

	if ch.hasErrorResponse() {
		t.Fatal("published spurious reason.response after interrupt")
	}
}

// ── Seed messages ───────────────────────────────────────────────────────────

// TestSeedMessagesAppliedAtConstruction verifies the spawner's handover brief
// becomes the transcript's first message (goal goes to Programs instead -
// tested on the swarm side).
func TestSeedMessagesAppliedAtConstruction(t *testing.T) {
	seed := []llm.Message{{
		Role: llm.RoleUser,
		Content: []llm.ContentBlock{{Type: llm.ContentText,
			Text: "[handover brief from spawner]\nroot cause found (trace=t_abc)"}},
	}}
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel(), SeedMessages: seed})

	msgs := w.Messages()
	if len(msgs) != 1 {
		t.Fatalf("seed not applied, got %d messages", len(msgs))
	}
	if !strings.Contains(msgs[0].Content[0].Text, "trace=t_abc") {
		t.Fatalf("brief content lost: %q", msgs[0].Content[0].Text)
	}
}

// TestSeedAbsentForFreshWorker verifies no seed leaves the transcript empty.
func TestSeedAbsentForFreshWorker(t *testing.T) {
	w := NewWorker(Config{ID: "r1", Bus: newMockChannel()})
	if got := len(w.Messages()); got != 0 {
		t.Fatalf("fresh worker should be empty, got %d", got)
	}
}
