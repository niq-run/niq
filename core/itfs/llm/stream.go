package llm

import (
	"context"
	"sync"
)

// StreamEventType identifies the kind of stream event.
type StreamEventType string

const (
	StreamStart         StreamEventType = "start"
	StreamTextStart     StreamEventType = "text_start"
	StreamTextDelta     StreamEventType = "text_delta"
	StreamTextEnd       StreamEventType = "text_end"
	StreamThinkingStart StreamEventType = "thinking_start"
	StreamThinkingDelta StreamEventType = "thinking_delta"
	StreamThinkingEnd   StreamEventType = "thinking_end"
	StreamToolCallStart StreamEventType = "tool_call_start"
	StreamToolCallDelta StreamEventType = "tool_call_delta"
	StreamToolCallEnd   StreamEventType = "tool_call_end"
	StreamDone          StreamEventType = "done"
	StreamError         StreamEventType = "error"
)

// StreamEvent is a single event in a streaming completion.
type StreamEvent interface {
	EventType() StreamEventType
}

// Concrete stream event types.

type EventStart struct{}

func (EventStart) EventType() StreamEventType { return StreamStart }

type EventTextStart struct{}

func (EventTextStart) EventType() StreamEventType { return StreamTextStart }

type EventTextDelta struct{ Delta string }

func (EventTextDelta) EventType() StreamEventType { return StreamTextDelta }

type EventTextEnd struct{}

func (EventTextEnd) EventType() StreamEventType { return StreamTextEnd }

type EventThinkingStart struct{ Signature string }

func (EventThinkingStart) EventType() StreamEventType { return StreamThinkingStart }

type EventThinkingDelta struct{ Delta string }

func (EventThinkingDelta) EventType() StreamEventType { return StreamThinkingDelta }

type EventThinkingEnd struct{ Redacted bool }

func (EventThinkingEnd) EventType() StreamEventType { return StreamThinkingEnd }

type EventToolCallStart struct{ ToolName string }

func (EventToolCallStart) EventType() StreamEventType { return StreamToolCallStart }

type EventToolCallDelta struct{ Delta string }

func (EventToolCallDelta) EventType() StreamEventType { return StreamToolCallDelta }

type EventToolCallEnd struct{ Arguments string }

func (EventToolCallEnd) EventType() StreamEventType { return StreamToolCallEnd }

type EventDone struct{ Message Message }

func (EventDone) EventType() StreamEventType { return StreamDone }

type EventError struct{ Err error }

func (EventError) EventType() StreamEventType { return StreamError }

// EventStream is a push-pull event stream.
// Provider pushes events via Push/End; consumer pulls via Next.
type EventStream struct {
	events chan StreamEvent
	result chan Message
	done   chan struct{}
	once   sync.Once
}

// NewEventStream creates a buffered EventStream ready for use.
func NewEventStream() *EventStream {
	return &EventStream{
		events: make(chan StreamEvent, 128),
		result: make(chan Message, 1),
		done:   make(chan struct{}),
	}
}

// Push sends a stream event. No-op after stream is closed.
func (es *EventStream) Push(evt StreamEvent) {
	select {
	case es.events <- evt:
	case <-es.done:
	}
}

// End marks the stream as complete with the final message.
func (es *EventStream) End(msg Message) {
	select {
	case es.result <- msg:
	default:
	}
	close(es.events)
	es.once.Do(func() { close(es.done) })
}

// Abort terminates the stream early with an error.
func (es *EventStream) Abort(err error) {
	select {
	case es.result <- Message{}:
	default:
	}
	es.Push(EventError{Err: err})
	close(es.events)
	es.once.Do(func() { close(es.done) })
}

// Next blocks until the next event is available.
// Returns the event and false when the stream is exhausted.
func (es *EventStream) Next() (StreamEvent, bool) {
	evt, ok := <-es.events
	return evt, ok
}

// Result blocks until the final Message is available.
func (es *EventStream) Result(ctx context.Context) (Message, error) {
	select {
	case msg := <-es.result:
		return msg, nil
	case <-ctx.Done():
		return Message{}, ctx.Err()
	}
}

// Drain consumes all remaining events. Useful for cleanup.
func (es *EventStream) Drain(ctx context.Context) error {
	for {
		select {
		case _, ok := <-es.events:
			if !ok {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// C returns the internal events channel for direct iteration in select blocks.
func (es *EventStream) C() <-chan StreamEvent {
	return es.events
}
