package iface

import (
	"context"
	"fmt"
	"sync"
	"time"

	corebus "github.com/niq-run/niq/core/itfs/bus"
	"github.com/niq-run/niq/core/itfs/event"
)

// handler is the Handler implementation handed to every backend. One instance
// per worker: it holds the outbound channel, the pending-request table used to
// pair replies, and the inbound subscriptions.
type handler struct {
	id      string
	ch      corebus.WorkerSideChannel
	timeout time.Duration

	mu      sync.Mutex
	pending map[string]chan event.Event // keyed by request id
	subs    map[*subscription]struct{}
	closed  bool
	// stopCh is closed when the worker stops, so in-flight requests fail with a
	// clear error instead of waiting out their timeout. It is separate from the
	// pending channels on purpose: closing those would race with a late reply.
	stopCh chan struct{}
}

func newHandler(id string, ch corebus.WorkerSideChannel, timeout time.Duration) *handler {
	return &handler{
		id:      id,
		ch:      ch,
		timeout: timeout,
		pending: map[string]chan event.Event{},
		subs:    map[*subscription]struct{}{},
		stopCh:  make(chan struct{}),
	}
}

func (h *handler) ID() string { return h.id }

func (h *handler) Send(ctx context.Context, evt event.Event, targets ...string) error {
	if len(targets) == 0 {
		return fmt.Errorf("iface: %s: send needs at least one target (use Broadcast for everyone)", h.id)
	}
	return h.ch.Send(ctx, evt, targets...)
}

func (h *handler) Broadcast(ctx context.Context, evt event.Event) error {
	return h.ch.Broadcast(ctx, evt)
}

// Request sends evt to target and waits for its reply. The caller's event is
// normalized in place: a trace id and a request id are assigned when absent, so
// the reply can be paired even when the caller did not think about correlation.
func (h *handler) Request(ctx context.Context, target string, evt event.Event) (event.Event, error) {
	if target == "" {
		return event.Event{}, fmt.Errorf("iface: %s: request needs a target", h.id)
	}
	if evt.TraceID == "" {
		evt.TraceID = evt.ID
	}
	if evt.RequestId == "" {
		evt.RequestId = evt.ID
	}

	ch := make(chan event.Event, 1)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return event.Event{}, fmt.Errorf("iface: %s is stopped", h.id)
	}
	h.pending[evt.RequestId] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, evt.RequestId)
		h.mu.Unlock()
	}()

	if err := h.ch.Send(ctx, evt, target); err != nil {
		return event.Event{}, fmt.Errorf("iface: send %s to %s: %w", evt.Type, target, err)
	}

	timer := time.NewTimer(h.timeout)
	defer timer.Stop()
	select {
	case reply := <-ch:
		if reply.Type == event.TypeRequestCompleted {
			return reply, nil
		}
		return reply, &ReplyError{Type: reply.Type, Message: replyMessage(reply, evt.Type, target)}
	case <-h.stopCh:
		return event.Event{}, fmt.Errorf("iface: %s stopped while waiting for a reply from %s", h.id, target)
	case <-timer.C:
		return event.Event{}, fmt.Errorf("iface: timed out after %s waiting for a reply from %s to %s", h.timeout, target, evt.Type)
	case <-ctx.Done():
		return event.Event{}, ctx.Err()
	}
}

// resolve delivers evt to the pending request it answers, reporting whether it
// answered one. Progress notifications are not terminal, so they are ignored
// here (a caller that wants them watches with OnEvent).
func (h *handler) resolve(evt event.Event) bool {
	switch evt.Type {
	case event.TypeRequestCompleted, event.TypeRequestFailed, event.TypeRequestRejected:
	default:
		return false
	}
	h.mu.Lock()
	ch, ok := h.pending[evt.RequestId]
	h.mu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- evt:
	default: // a reply already arrived; the first one wins
	}
	return true
}

// ReplyError is a failed or rejected reply. It carries the reply event so a
// caller can inspect whatever else the target said.
type ReplyError struct {
	Type    event.EventType
	Message string
}

func (e *ReplyError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("iface: reply %s without a message", e.Type)
	}
	return e.Message
}

func replyMessage(reply event.Event, requested event.EventType, target string) string {
	if msg, ok := reply.Payload["error"].(string); ok && msg != "" && msg != "<nil>" {
		return msg
	}
	if msg, ok := reply.Payload["reason"].(string); ok && msg != "" {
		return msg
	}
	return fmt.Sprintf("%s to %s was answered with %s", requested, target, reply.Type)
}

// OnEvent registers an inbound interest. Delivery is per subscription, in
// arrival order, on the subscription's own goroutine, so one slow backend cannot
// stall another (or the bus loop).
func (h *handler) OnEvent(fn func(event.Event), types ...event.EventType) Subscription {
	s := &subscription{
		handler: h,
		fn:      fn,
		types:   types,
		ch:      make(chan event.Event, subscriptionQueue),
		done:    make(chan struct{}),
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(s.done)
		return s
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	go s.run()
	return s
}

// subscriptionQueue is how many events may be in flight to one subscriber. A
// subscriber that falls further behind drops events (and says so) rather than
// growing without bound or blocking the worker's loop.
const subscriptionQueue = 256

type subscription struct {
	handler *handler
	fn      func(event.Event)
	types   []event.EventType
	ch      chan event.Event
	done    chan struct{}
	once    sync.Once
}

func (s *subscription) run() {
	for {
		select {
		case evt, ok := <-s.ch:
			if !ok {
				return
			}
			s.deliver(evt)
		case <-s.done:
			return
		}
	}
}

// deliver isolates a failing subscriber: a panic in one backend must not take
// down the worker's event loop (and with it every other backend).
func (s *subscription) deliver(evt event.Event) {
	if s.fn == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			logf("%s: subscriber panic on %s: %v", s.handler.id, evt.Type, r)
		}
	}()
	s.fn(evt)
}

// Close detaches the subscription.
func (s *subscription) Close() {
	s.once.Do(func() {
		s.handler.mu.Lock()
		delete(s.handler.subs, s)
		s.handler.mu.Unlock()
		close(s.done)
	})
}

// matches reports whether the subscription wants evt.
func (s *subscription) matches(evt event.Event) bool {
	if len(s.types) == 0 {
		return true
	}
	for _, t := range s.types {
		if t == "*" || t == evt.Type {
			return true
		}
	}
	return false
}

// publish fans evt out to every interested subscription.
func (h *handler) publish(evt event.Event) {
	h.mu.Lock()
	subs := make([]*subscription, 0, len(h.subs))
	for s := range h.subs {
		if s.matches(evt) {
			subs = append(subs, s)
		}
	}
	h.mu.Unlock()

	for _, s := range subs {
		select {
		case s.ch <- evt:
		default:
			logf("%s: dropping %s for a slow subscriber", h.id, evt.Type)
		}
	}
}

// close stops every subscription and unblocks in-flight requests.
func (h *handler) close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	close(h.stopCh)
	subs := make([]*subscription, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.subs = map[*subscription]struct{}{}
	h.mu.Unlock()

	for _, s := range subs {
		s.Close()
	}
}
