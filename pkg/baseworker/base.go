// Package baseworker provides the shared base implementation every built-in
// Go worker embeds: identity, subscriptions, the worker-side bus channel,
// tool-request reply plumbing, and the extension registry — the uniform way
// for a worker to declare what it responds to and how.
//
// It is deliberately an implementation package. The contracts it partially
// implements (Worker / ManagedWorker) stay in core/worker, as do the shared
// data vocabulary types (Tool, ToolFunc).
package baseworker

import (
	"context"

	corebus "github.com/niq-run/niq/core/bus"

	"github.com/niq-run/niq/core/event"
)

// BaseWorker provides a partial implementation of the worker contract
// (core/worker.Worker) that other workers embed. It stores an id, a
// subscription list and the extension registry; Start is an intentional
// no-op that callers are expected to override.
type BaseWorker struct {
	id      string
	subs    []event.EventPattern
	Channel corebus.WorkerSideChannel

	// extensions is the registry filled by Register. Held behind a pointer
	// (see extensionRegistry): the lock lives in the registry, not here, so
	// BaseWorker stays copyable by value. Registration is safe at any time,
	// including at runtime from within a handler.
	extensions *extensionRegistry
}

// NewBaseWorker creates a BaseWorker with the given id, subscriptions and
// worker-side channel to the event bus.
func NewBaseWorker(id string, subs []event.EventPattern, ch corebus.WorkerSideChannel) BaseWorker {
	return BaseWorker{
		id:         id,
		subs:       subs,
		Channel:    ch,
		extensions: &extensionRegistry{regs: make(map[string]registeredExtension)},
	}
}

func (w *BaseWorker) ID() string { return w.id }

// Subscriptions returns the event patterns this worker is interested in.
// Implements the Worker contract.
func (w *BaseWorker) Subscriptions() []event.EventPattern {
	return w.subs
}

// Start is a no-op stub. Embedding workers should override.
func (w *BaseWorker) Start(ctx context.Context) error {
	return nil
}

// ── Tool-serving helpers ──
//
// These cover the repetitive parts every worker that exposes tools over the
// bus repeats: parsing a tool.request event and replying with a completed
// or failed result. Workers embed BaseWorker and call these directly.

// ToolCall holds the parsed, common fields of a tool.request event.
type ToolCall struct {
	CallID   string
	Name     string
	CallerID string
	Args     map[string]any
	TraceID  string
}

// ParseToolCall extracts the common fields from a tool.request event.
// Args is always a non-nil map so handlers can write into it safely.
func ParseToolCall(evt event.Event) ToolCall {
	args, _ := evt.Payload["arguments"].(map[string]any)
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{
		CallID:   ArgString(evt.Payload, "call_id"),
		Name:     ArgString(evt.Payload, "name"),
		CallerID: evt.WorkerId,
		Args:     args,
		TraceID:  evt.TraceID,
	}
}

// ReplyCompleted replies to a tool.request caller with a tool.completed
// result, propagating the trace ID.
func (w *BaseWorker) ReplyCompleted(callerID, callID, name, result, traceID string) {
	evt := event.New(event.TypeToolCompleted, w.ID(), map[string]any{
		"call_id": callID,
		"name":    name,
		"result":  result,
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// ReplyFailed replies to a tool.request caller with a tool.failed error
// message, propagating the trace ID.
func (w *BaseWorker) ReplyFailed(callerID, callID, name, errMsg, traceID string) {
	evt := event.New(event.TypeToolFailed, w.ID(), map[string]any{
		"call_id": callID,
		"name":    name,
		"error":   errMsg,
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// ReplyRejected replies to a tool.request caller with a tool.rejected
// event, carrying the reason. It is used when a worker declines a call based
// on its own rules (e.g. a safety guard) or after a human-in-the-loop approval
// is denied. Propagates the trace ID.
func (w *BaseWorker) ReplyRejected(callerID, callID, name, reason, traceID string) {
	evt := event.New(event.TypeToolRejected, w.ID(), map[string]any{
		"call_id": callID,
		"name":    name,
		"reason":  reason,
	})
	evt.TraceID = traceID
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// ReplyUnknownTool replies to a tool.request whose name no handler matched.
func (w *BaseWorker) ReplyUnknownTool(tc ToolCall) {
	w.ReplyFailed(tc.CallerID, tc.CallID, tc.Name, "unknown tool: "+tc.Name, tc.TraceID)
}

// ArgString returns the string value of a tool argument, or "" if absent.
func ArgString(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

// ArgInt returns the int value of a tool argument, or def if absent.
// JSON numbers decode as float64, so all numeric forms are accepted.
func ArgInt(args map[string]any, key string, def int) int {
	v, ok := args[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}
