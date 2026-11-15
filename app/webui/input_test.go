package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/niq-run/niq/core/impl/iface"
	"github.com/niq-run/niq/core/itfs/event"
)

// inputStubChannel records what POST /api/input publishes, and how. It is the
// bus handle the app receives from its interface worker.
type inputStubChannel struct {
	sent       []event.Event
	targets    [][]string
	broadcasts []event.Event
}

func (s *inputStubChannel) ID() string { return "stub" }
func (s *inputStubChannel) Send(_ context.Context, evt event.Event, targets ...string) error {
	s.sent = append(s.sent, evt)
	s.targets = append(s.targets, append([]string(nil), targets...))
	return nil
}
func (s *inputStubChannel) Broadcast(_ context.Context, evt event.Event) error {
	s.broadcasts = append(s.broadcasts, evt)
	return nil
}
func (s *inputStubChannel) Receive(context.Context) (<-chan event.Event, error) {
	return make(chan event.Event), nil
}
func (s *inputStubChannel) Close() error { return nil }

// inputServer wires a Server with the stub as its injected bus handler, the way
// the interface worker would inject it.
func inputServer(ch *inputStubChannel) *Server {
	s := &Server{}
	s.SetHandler(stubHandler{ch: ch})
	return s
}

// stubHandler adapts a recording channel to iface.Handler.
type stubHandler struct{ ch *inputStubChannel }

func (h stubHandler) ID() string { return "webui" }
func (h stubHandler) Send(_ context.Context, evt event.Event, targets ...string) error {
	return h.ch.Send(context.Background(), evt, targets...)
}
func (h stubHandler) Broadcast(_ context.Context, evt event.Event) error {
	return h.ch.Broadcast(context.Background(), evt)
}
func (h stubHandler) Request(context.Context, string, event.Event) (event.Event, error) {
	return event.Event{}, nil
}
func (h stubHandler) OnEvent(func(event.Event), ...event.EventType) iface.Subscription {
	return nopSub{}
}

type nopSub struct{}

func (nopSub) Close() {}

func postInput(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/input", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.handleInput(rec, req)
	return rec
}

// The input is addressed to the @-mentioned workers. A body that names nobody
// must be refused: falling through to a broadcast would deliver the user's
// message to every subscribed worker (the "I @-ed one worker but everyone got
// it" bug).
func TestHandleInputRequiresTargets(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	rec := postInput(t, s, `{"content":[{"type":"text","text":"hi"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(ch.sent) != 0 || len(ch.broadcasts) != 0 {
		t.Fatalf("sent=%d broadcasts=%d, want 0/0", len(ch.sent), len(ch.broadcasts))
	}
}

func TestHandleInputMultiTarget(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	rec := postInput(t, s, `{"content":[{"type":"text","text":"hi"}],"targets":["a","b"],"input_mode":"append"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(ch.sent) != 1 || len(ch.broadcasts) != 0 {
		t.Fatalf("sent=%d broadcasts=%d, want 1/0", len(ch.sent), len(ch.broadcasts))
	}
	if got := ch.targets[0]; len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("targets = %v, want [a b]", got)
	}
	if mode := ch.sent[0].Payload["input_mode"]; mode != "append" {
		t.Fatalf("input_mode = %v, want append", mode)
	}
}

// `target` is the pre-multi-target spelling; older clients still send it.
func TestHandleInputLegacySingleTarget(t *testing.T) {
	ch := &inputStubChannel{}
	s := inputServer(ch)

	rec := postInput(t, s, `{"content":[{"type":"text","text":"hi"}],"target":"a"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if len(ch.sent) != 1 || len(ch.broadcasts) != 0 {
		t.Fatalf("sent=%d broadcasts=%d, want 1/0", len(ch.sent), len(ch.broadcasts))
	}
	if got := ch.targets[0]; len(got) != 1 || got[0] != "a" {
		t.Fatalf("targets = %v, want [a]", got)
	}
}
