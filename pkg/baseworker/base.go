// Package baseworker provides the shared base implementation every built-in
// Go worker embeds: identity, subscriptions, the worker-side bus channel,
// tool-request reply plumbing, the extension registry — the uniform way for a
// worker to declare what it responds to and how — and durable-change
// signalling, the way a worker tells its owner to persist it.
//
// It is deliberately an implementation package. The contracts it partially
// implements (Worker / ManagedWorker) stay in core/worker, as do the shared
// data vocabulary types (Tool, ToolFunc).
package baseworker

import (
	"context"
	"log"
	"strings"

	corebus "github.com/niq-run/niq/core/bus"

	"github.com/niq-run/niq/core/event"
)

// BaseWorker provides a partial implementation of the worker contract
// (core/worker.Worker) that other workers embed. It stores an id and the
// extension registry; Start is an intentional no-op that callers are expected
// to override.
//
// Note on subscriptions: what a worker receives over broadcast is the
// identity's SubscribeAllow — a control-plane attribute declared in the
// worker's spec (and manageable via the control plane), registered before the
// worker process exists. It is deliberately NOT declared on the worker object:
// a worker able to grant itself subscriptions could bypass bus-level
// distribution authorization.
type BaseWorker struct {
	id      string
	Channel corebus.WorkerSideChannel

	// extensions is the registry filled by Register. Held behind a pointer
	// (see extensionRegistry): the lock lives in the registry, not here, so
	// BaseWorker stays copyable by value. Registration is safe at any time,
	// including at runtime from within a handler.
	extensions *extensionRegistry

	// durability is the durable-change signal (see durable.go). Behind a
	// pointer for the same reason as extensions: copies share one channel.
	durability *durability
}

// NewBaseWorker creates a BaseWorker with the given id and worker-side
// channel to the event bus.
func NewBaseWorker(id string, ch corebus.WorkerSideChannel) BaseWorker {
	return BaseWorker{
		id:         id,
		Channel:    ch,
		extensions: &extensionRegistry{regs: make(map[string]registeredExtension)},
		durability: &durability{ch: make(chan struct{}, 1)},
	}
}

func (w *BaseWorker) ID() string { return w.id }

// Start is a no-op stub. Embedding workers should override.
func (w *BaseWorker) Start(ctx context.Context) error {
	return nil
}

// ── Tool-serving helpers ──
//
// These cover the repetitive parts every worker that exposes tools over the
// bus repeats: parsing a tool-invocation event and replying with a
// request.completed / request.failed / request.rejected result. Workers embed
// BaseWorker and call these directly. The replies carry no tool name: the
// caller identifies the tool by correlating the echoed request_id with the
// invocation (whose event type IS the tool).

// ToolCall holds the parsed, common fields of a tool-invocation event.
type ToolCall struct {
	CallID   string
	Name     string
	CallerID string
	Args     map[string]any
	TraceID  string
}

// ParseToolCall extracts the common fields from a tool-invocation event. The
// event type IS the tool, so Name comes from evt.Type; Args is the event's
// top-level payload (there is no wrapper — payload is the argument object).
// Args is always a non-nil map so handlers can write into it safely.
func ParseToolCall(evt event.Event) ToolCall {
	args := evt.Payload
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{
		CallID:   evt.RequestId,
		Name:     string(evt.Type),
		CallerID: evt.WorkerId,
		Args:     args,
		TraceID:  evt.TraceID,
	}
}

// ReplyCompleted answers a request caller with a request.completed result,
// echoing the request's id and propagating the trace ID.
func (w *BaseWorker) ReplyCompleted(callerID, callID, result, traceID string) {
	evt := event.New(event.TypeRequestCompleted, w.ID(), map[string]any{
		"result": result,
	})
	evt.RequestId = callID
	evt.TraceID = traceID
	log.Printf("[baseworker] reply EMIT type=request.completed worker=%s caller=%s request_id=%s result_len=%d",
		w.ID(), callerID, callID, len(result))
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// ReplyFailed answers a request caller with a request.failed error message,
// echoing the request's id and propagating the trace ID.
func (w *BaseWorker) ReplyFailed(callerID, callID, errMsg, traceID string) {
	w.replyResult(event.TypeRequestFailed, callerID, callID, map[string]any{"error": errMsg}, traceID, false)
}

// ReplyCompletedTransient is like ReplyCompleted but marks the reply Transient
// (streamed live, not persisted). Read-only queries (program search/load,
// provider list/current, mount.list, list_workers, ...) use this so their
// results don't crowd durable history — the reply is still delivered live.
func (w *BaseWorker) ReplyCompletedTransient(callerID, callID, result, traceID string) {
	w.replyResult(event.TypeRequestCompleted, callerID, callID, map[string]any{"result": result}, traceID, true)
}

// ReplyFailedTransient is the Transient variant of ReplyFailed.
func (w *BaseWorker) ReplyFailedTransient(callerID, callID, errMsg, traceID string) {
	w.replyResult(event.TypeRequestFailed, callerID, callID, map[string]any{"error": errMsg}, traceID, true)
}

// replyResult forges and sends one request.* reply.
func (w *BaseWorker) replyResult(typ event.EventType, callerID, callID string, payload map[string]any, traceID string, transient bool) {
	evt := event.New(typ, w.ID(), payload)
	evt.RequestId = callID
	evt.TraceID = traceID
	evt.Transient = transient
	log.Printf("[baseworker] reply EMIT type=%s worker=%s caller=%s request_id=%s transient=%v",
		typ, w.ID(), callerID, callID, transient)
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// ReplyRejected answers a request caller with a request.rejected event,
// carrying the reason. It is used when a worker declines a call based on its
// own rules (e.g. a safety guard) or after a human-in-the-loop approval is
// denied. Echoes the request's id and propagates the trace ID.
func (w *BaseWorker) ReplyRejected(callerID, callID, reason, traceID string) {
	evt := event.New(event.TypeRequestRejected, w.ID(), map[string]any{
		"reason": reason,
	})
	evt.RequestId = callID
	evt.TraceID = traceID
	log.Printf("[baseworker] reply EMIT type=request.rejected worker=%s caller=%s request_id=%s reason_len=%d",
		w.ID(), callerID, callID, len(reason))
	_ = w.Channel.Send(context.Background(), evt, callerID)
}

// ReplyUnknownTool replies to a tool-invocation whose name no handler matched.
func (w *BaseWorker) ReplyUnknownTool(tc ToolCall) {
	w.ReplyFailed(tc.CallerID, tc.CallID, "unknown tool: "+tc.Name, tc.TraceID)
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

// ArgStrings returns the string values of a tool argument as a slice, or nil
// when absent. Accepts a JSON array of strings, an array of any, or a single
// comma-separated string (e.g. tags from the spawn tool).
func ArgStrings(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch items := v.(type) {
	case string:
		if strings.TrimSpace(items) == "" {
			return nil
		}
		var out []string
		for _, s := range strings.Split(items, ",") {
			if t := strings.TrimSpace(s); t != "" {
				out = append(out, t)
			}
		}
		return out
	case []string:
		return items
	case []any:
		var out []string
		for _, it := range items {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}
