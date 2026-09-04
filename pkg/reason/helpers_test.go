// Shared test facilities for pkg/reason: a mock bus channel (testChannel),
// deterministic LLM providers (staticProvider, blockingProvider), a worker
// factory (newTestWorker) and the startWorker integration harness, plus
// waitCond/testTimeout for polling async behavior.
package reason

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/niq-run/niq/core/event"
	llm "github.com/niq-run/niq/core/llm"
)

type testChannel struct {
	in  chan event.Event
	mu  sync.Mutex
	out []event.Event
}

func newTestChannel() *testChannel { return &testChannel{in: make(chan event.Event, 16)} }

func (m *testChannel) ID() string { return "mock" }
func (m *testChannel) Send(_ context.Context, evt event.Event, _ ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, evt)
	return nil
}
func (m *testChannel) Broadcast(_ context.Context, evt event.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, evt)
	return nil
}
func (m *testChannel) Receive(_ context.Context) (<-chan event.Event, error) { return m.in, nil }
func (m *testChannel) Close() error                                          { return nil }

func (m *testChannel) eventsOf(typ event.EventType) []event.Event {
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

func (m *testChannel) hasInterrupted() bool {
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

const testTimeout = 2 * time.Second

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

// newTestWorker constructs a bare BaseReasonWorker wired to the test provider
// and channel (nil args get defaults). Used where a test just needs a worker
// instance without starting its watch loop.
func newTestWorker(provider llm.LLMProvider, ch *testChannel) *BaseReasonWorker {
	if ch == nil {
		ch = newTestChannel()
	}
	return NewBaseReasonWorker(Config{
		ID:       "r1",
		Provider: provider,
		Bus:      ch,
	})
}

// staticProvider returns a fixed completion immediately.
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

// blockingProvider holds CompleteStream until release, for holding a round in
// flight.
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

// startWorker builds a worker and starts its full watch loop, so integration
// tests can feed events via ch.in and observe published events via ch. Returns
// the running worker, its channel, and a cancel func; the worker is stopped
// and cancelled on test cleanup.
func startWorker(t *testing.T, prov llm.LLMProvider) (*BaseReasonWorker, *testChannel, context.CancelFunc) {
	t.Helper()
	ch := newTestChannel()
	w := newTestWorker(prov, ch)
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { w.Stop(); cancel() })
	return w, ch, cancel
}
