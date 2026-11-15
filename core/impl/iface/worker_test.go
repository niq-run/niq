package iface

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// pipe is a WorkerSideChannel double: Send/Broadcast record and can answer, and
// the test drives inbound events through in.
type pipe struct {
	mu     sync.Mutex
	sent   []event.Event
	target []string
	// answer, when set, is called for each Send and may push a reply back.
	answer func(p *pipe, evt event.Event)
	in     chan event.Event
	closed bool
}

func newPipe() *pipe { return &pipe{in: make(chan event.Event, 16)} }

func (p *pipe) ID() string { return "test" }

func (p *pipe) Send(ctx context.Context, evt event.Event, targets ...string) error {
	p.mu.Lock()
	p.sent = append(p.sent, evt)
	p.target = append(p.target, targets...)
	answer := p.answer
	p.mu.Unlock()
	if answer != nil {
		answer(p, evt)
	}
	return nil
}

func (p *pipe) Broadcast(ctx context.Context, evt event.Event) error {
	p.mu.Lock()
	p.sent = append(p.sent, evt)
	p.mu.Unlock()
	return nil
}

func (p *pipe) Receive(ctx context.Context) (<-chan event.Event, error) { return p.in, nil }

func (p *pipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.in)
	}
	return nil
}

func (p *pipe) push(evt event.Event) { p.in <- evt }

func (p *pipe) lastSent() (event.Event, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sent) == 0 {
		return event.Event{}, false
	}
	return p.sent[len(p.sent)-1], true
}

// fakeBackend records injection and inbound delivery.
type fakeBackend struct {
	mu     sync.Mutex
	h      Handler
	inbox  []event.Event
	gotH   chan struct{}
	noisy  func(event.Event)
	states [][]byte
}

func newFakeBackend() *fakeBackend { return &fakeBackend{gotH: make(chan struct{})} }

func (b *fakeBackend) SetHandler(h Handler) {
	b.mu.Lock()
	b.h = h
	b.mu.Unlock()
	close(b.gotH)
}

func (b *fakeBackend) handler(t *testing.T) Handler {
	t.Helper()
	select {
	case <-b.gotH:
	case <-time.After(2 * time.Second):
		t.Fatal("SetHandler was never called")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.h
}

func (b *fakeBackend) seen() []event.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]event.Event(nil), b.inbox...)
}

// startWorker builds and starts a worker over a pipe, returning both.
func startWorker(t *testing.T, cfg Config) (*Worker, *pipe) {
	t.Helper()
	p := newPipe()
	cfg.Bus = p
	w := New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = w.Stop() })
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return w, p
}

// waitFor polls until cond holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestBackendIsInjectedBeforeStart verifies SetHandler runs before any event can
// be delivered, so a backend can build its outward side knowing how to reach in.
func TestBackendIsInjectedBeforeStart(t *testing.T) {
	be := newFakeBackend()
	w, p := startWorker(t, Config{ID: "w1", Type: "interface", Backends: []Backend{be}})

	h := be.handler(t)
	if h.ID() != "w1" {
		t.Fatalf("handler id = %q", h.ID())
	}
	// Presence is announced on start; the id the backend speaks as is the worker's.
	first, ok := p.lastSent()
	if !ok || first.Type != event.TypeWorkerDiscover {
		t.Fatalf("no presence announce: %v", ok)
	}
	if w.Type() != "interface" {
		t.Fatalf("type = %q", w.Type())
	}
}

// TestInboundFanOutByType verifies both the wildcard and the typed
// registrations, and that one event reaches every interested backend.
func TestInboundFanOutByType(t *testing.T) {
	all, typed := newFakeBackend(), newFakeBackend()
	_, p := startWorker(t, Config{ID: "w1", Backends: []Backend{all, typed}})

	allHandler, typedHandler := all.handler(t), typed.handler(t)
	var allSeen, typedSeen []event.Event
	var mu sync.Mutex
	allHandler.OnEvent(func(e event.Event) { mu.Lock(); allSeen = append(allSeen, e); mu.Unlock() })
	typedHandler.OnEvent(func(e event.Event) { mu.Lock(); typedSeen = append(typedSeen, e); mu.Unlock() }, event.TypeWorkerInput)

	p.push(event.New(event.TypeWorkerInput, "reason", map[string]any{"n": 1}))
	p.push(event.New("worker.ready", "reason", nil))

	waitFor(t, "both deliveries", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(allSeen) == 2 && len(typedSeen) == 1
	})
	mu.Lock()
	defer mu.Unlock()
	if typedSeen[0].Type != event.TypeWorkerInput {
		t.Fatalf("typed subscriber got %s", typedSeen[0].Type)
	}
}

// TestSubscriptionCloseStopsDelivery verifies a backend can detach, and that
// Close is idempotent.
func TestSubscriptionCloseStopsDelivery(t *testing.T) {
	be := newFakeBackend()
	_, p := startWorker(t, Config{ID: "w1", Backends: []Backend{be}})

	var mu sync.Mutex
	var got int
	sub := be.handler(t).OnEvent(func(event.Event) { mu.Lock(); got++; mu.Unlock() })

	p.push(event.New(event.TypeWorkerInput, "reason", nil))
	waitFor(t, "first delivery", func() bool { mu.Lock(); defer mu.Unlock(); return got == 1 })

	sub.Close()
	sub.Close() // idempotent
	p.push(event.New(event.TypeWorkerInput, "reason", nil))
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if got != 1 {
		t.Fatalf("delivered %d events after Close", got)
	}
}

// TestRequestPairsReply verifies the outbound request/reply convenience: ids are
// assigned when the caller omits them, the reply resolves the wait, and a
// request.failed becomes an error carrying the target's message.
func TestRequestPairsReply(t *testing.T) {
	be := newFakeBackend()
	p := newPipe()
	p.answer = func(p *pipe, evt event.Event) {
		replyType := event.TypeRequestCompleted
		payload := map[string]any{"ok": true}
		if evt.Payload["fail"] == true {
			replyType = event.TypeRequestFailed
			payload = map[string]any{"error": "nope"}
		}
		reply := event.New(replyType, "peer", payload)
		reply.RequestId = evt.RequestId
		reply.TraceID = evt.TraceID
		go p.push(reply)
	}
	w := New(Config{ID: "w1", Bus: p, Backends: []Backend{be}, RequestTimeout: 2 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = w.Stop() })
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	h := be.handler(t)

	reply, err := h.Request(ctx, "peer", event.New("spawn", "w1", map[string]any{"type": "reason"}))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply.Payload["ok"] != true {
		t.Fatalf("reply = %+v", reply.Payload)
	}

	sent, _ := p.lastSent()
	if sent.TraceID == "" || sent.RequestId == "" {
		t.Fatalf("ids not assigned: %+v", sent)
	}

	_, err = h.Request(ctx, "peer", event.New("spawn", "w1", map[string]any{"fail": true}))
	var rerr *ReplyError
	if !errors.As(err, &rerr) || rerr.Message != "nope" {
		t.Fatalf("err = %v, want a ReplyError carrying %q", err, "nope")
	}
}

// TestRequestTimeoutAndStop verifies both failure modes: an unanswered request
// times out, and one in flight when the worker stops fails immediately rather
// than waiting out the timeout.
func TestRequestTimeoutAndStop(t *testing.T) {
	be := newFakeBackend()
	startWorker(t, Config{ID: "w1", Backends: []Backend{be}, RequestTimeout: 60 * time.Millisecond})
	h := be.handler(t)

	_, err := h.Request(context.Background(), "peer", event.New("spawn", "w1", nil))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}

	// In flight when the worker stops: a long timeout, then Stop.
	w2be := newFakeBackend()
	w2, _ := startWorker(t, Config{ID: "w2", Backends: []Backend{w2be}, RequestTimeout: 10 * time.Second})
	h2 := w2be.handler(t)
	done := make(chan error, 1)
	go func() {
		_, err := h2.Request(context.Background(), "peer", event.New("spawn", "w2", nil))
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if err := w2.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Fatalf("err = %v, want a stop error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request did not fail on Stop")
	}
}

// TestToolsAndMessagesAreSeparated verifies the loop's two halves: a registered
// tool answers and does NOT reach the backends, while a plain message does.
func TestToolsAndMessagesAreSeparated(t *testing.T) {
	be := newFakeBackend()
	w, p := startWorker(t, Config{ID: "w1", Backends: []Backend{be}})
	w.Register(baseworker.Extension{Event: "iface.ping", Description: "test tool"}, func(evt event.Event) {
		w.ReplyCompleted(evt.WorkerId, evt.RequestId, map[string]any{"pong": true}, evt.TraceID)
	})

	var mu sync.Mutex
	var inbound []event.Event
	be.handler(t).OnEvent(func(e event.Event) { mu.Lock(); inbound = append(inbound, e); mu.Unlock() })

	req := event.New("iface.ping", "peer", nil)
	req.RequestId = "r1"
	p.push(req)
	waitFor(t, "tool reply", func() bool {
		last, ok := p.lastSent()
		return ok && last.Type == event.TypeRequestCompleted
	})

	p.push(event.New(event.TypeWorkerInput, "reason", nil))
	waitFor(t, "message delivery", func() bool { mu.Lock(); defer mu.Unlock(); return len(inbound) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if inbound[0].Type != event.TypeWorkerInput {
		t.Fatalf("backend saw %s; a served tool must not reach it", inbound[0].Type)
	}
}

// TestSubscriberPanicIsIsolated verifies one broken backend cannot take down
// delivery to the others.
func TestSubscriberPanicIsIsolated(t *testing.T) {
	bad, good := newFakeBackend(), newFakeBackend()
	_, p := startWorker(t, Config{ID: "w1", Backends: []Backend{bad, good}})

	bad.handler(t).OnEvent(func(event.Event) { panic("boom") })
	var mu sync.Mutex
	var n int
	good.handler(t).OnEvent(func(event.Event) { mu.Lock(); n++; mu.Unlock() })

	p.push(event.New(event.TypeWorkerInput, "reason", nil))
	p.push(event.New(event.TypeWorkerInput, "reason", nil))
	waitFor(t, "healthy subscriber deliveries", func() bool { mu.Lock(); defer mu.Unlock(); return n == 2 })
}

// TestSnapshotDelegatesToBackend verifies the worker keeps no state of its own:
// what survives a restart is whatever the injected state provider holds.
func TestSnapshotDelegatesToBackend(t *testing.T) {
	be := newFakeBackend()
	state := &fakeState{}
	_ = be
	w := New(Config{ID: "w1", Bus: newPipe(), Backends: []Backend{state}})

	state.value = []byte(`{"local":"a"}`)
	snap, err := w.Snapshot()
	if err != nil || string(snap) != `{"local":"a"}` {
		t.Fatalf("Snapshot = %q, %v", snap, err)
	}
	if err := w.Restore([]byte(`{"local":"b"}`)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if string(state.value) != `{"local":"b"}` {
		t.Fatalf("state = %q", state.value)
	}

	// No state provider: nothing to persist, and restore is a no-op.
	if snap, err := New(Config{ID: "w2", Bus: newPipe()}).Snapshot(); err != nil || snap != nil {
		t.Fatalf("Snapshot without state = %q, %v", snap, err)
	}
}

// fakeState is a backend that is also the worker's durable state.
type fakeState struct{ value []byte }

func (s *fakeState) SetHandler(Handler)        {}
func (s *fakeState) Snapshot() ([]byte, error) { return s.value, nil }
func (s *fakeState) Restore(b []byte) error    { s.value = b; return nil }

// TestRunnableBackendGetsItsOwnLifetime verifies a backend that needs a lifetime
// (a link, a render loop) is started after injection and stopped before the bus
// is released — and that a failing start fails the worker, since a deaf worker
// must not come up.
func TestRunnableBackendGetsItsOwnLifetime(t *testing.T) {
	be := &runnableBackend{failStart: false}
	w := New(Config{ID: "w1", Bus: newPipe(), Backends: []Backend{be}})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	be.mu.Lock()
	if !be.setHandler || !be.started {
		t.Fatalf("setHandler=%v started=%v, want injection before start", be.setHandler, be.started)
	}
	be.mu.Unlock()

	if err := w.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	be.mu.Lock()
	defer be.mu.Unlock()
	if !be.stopped {
		t.Fatal("backend was not stopped")
	}

	bad := &runnableBackend{failStart: true}
	if err := New(Config{ID: "w2", Bus: newPipe(), Backends: []Backend{bad}}).Start(ctx); err == nil {
		t.Fatal("a backend whose Start failed did not fail the worker")
	}
}

type runnableBackend struct {
	mu         sync.Mutex
	setHandler bool
	started    bool
	stopped    bool
	failStart  bool
}

func (b *runnableBackend) SetHandler(Handler) { b.mu.Lock(); b.setHandler = true; b.mu.Unlock() }

func (b *runnableBackend) Start(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failStart {
		return errors.New("cannot start")
	}
	b.started = true
	return nil
}

func (b *runnableBackend) Stop() error { b.mu.Lock(); b.stopped = true; b.mu.Unlock(); return nil }
