// The default reason worker's extension toolkit: the generic tools
// (send_message / list_workers) and the context meta ops (compress / rotate).
// These are deliberately outside pkg/reason — they are the default worker's
// own choice of tools, not part of the shared reasoning mechanism. They are
// implemented entirely through the mechanism's exported accessors
// (ReplyCompleted / ReplyFailed / DiscoveredWorkers / CurrentTraceID /
// Transcript / TryReason / Channel), so nothing here reaches into unexported
// state. The
// context.compress / context.rotate handlers are this worker's own context
// strategy: they edit the transcript directly and book their own completion.
package reason

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/event"
	"github.com/niq-run/niq/pkg/baseworker"
	reasonBase "github.com/niq-run/niq/pkg/reason"
)

// registerDefaultExtensions registers the toolkit the generic reason worker
// adds on top of the base extensions (provider.switch / provider.list /
// provider.current): send_message, list_workers, context.compress,
// context.rotate. A reason-family worker that wants a different toolkit simply
// does not call this. compactDirective is the program-provided summarizer
// override (empty for the built-in fallback); it is owned by this worker, not
// the mechanism, and captured here so the context strategy can resolve it.
func registerDefaultExtensions(w *reasonBase.BaseReasonWorker, compactDirective string) {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event: event.TypeToolRequest, KeyField: "name", Key: "send_message",
		// SelfOnly: a worker's own addressed-messaging tool is not something
		// peers should be able to call, so it is announced only to itself.
		SelfOnly:    true,
		Description: "Send a message to a specific worker on the bus.",
		Parameters: obj(map[string]any{
			"target": map[string]any{"type": "string", "description": "Target worker ID"},
			"text":   map[string]any{"type": "string", "description": "Message text"},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		handleSendMessage(w, tc.CallID, tc.Name, tc.CallerID, tc.TraceID, tc.Args)
	})

	w.Register(baseworker.Extension{
		Event: event.TypeToolRequest, KeyField: "name", Key: "list_workers",
		// SelfOnly: list_workers reports this worker's own contract and is not
		// exposed to peers.
		SelfOnly:    true,
		Description: "List all available workers and their capabilities.",
		Parameters:  obj(map[string]any{}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		handleListWorkers(w, tc.CallID, tc.Name, tc.CallerID, tc.TraceID, tc.Args)
	})

	w.Register(baseworker.Extension{
		// The convention's op name comes from the mechanism, so the emitter
		// (emitContextCompress) and this handler cannot drift apart.
		Event: event.TypeWorkerUpdate, KeyField: "op", Key: reasonBase.ContextCompressOpEvent,
		Description: "Compact your own context history: older messages are replaced by a summary, the most recent messages are kept.",
		Parameters: obj(map[string]any{
			"directive": map[string]any{"type": "string",
				"description": "Optional focus for the summary, e.g. what must be preserved."},
		}),
	}, func(evt event.Event) {
		handleContextOp(w, evt, compactDirective)
	})

	w.Register(baseworker.Extension{
		Event: event.TypeWorkerUpdate, KeyField: "op", Key: "context.rotate",
		Description: "Rotate your context: summarize the current transcript as a carried digest and start a fresh context.",
		Parameters: obj(map[string]any{
			"carry": map[string]any{"type": "string",
				"description": "Optional instruction for what to carry into the digest."},
		}),
	}, func(evt event.Event) {
		handleContextOp(w, evt, compactDirective)
	})
}

// handleSendMessage serves the send_message tool: forwards the text to the
// target worker as a worker.input event and replies with a tool result.
func handleSendMessage(w *reasonBase.BaseReasonWorker, callID, toolName, callerID, traceID string, args map[string]any) {
	target, _ := args["target"].(string)
	text, _ := args["text"].(string)
	if target == "" || text == "" {
		w.ReplyFailed(callerID, callID, toolName, "target and text are required", traceID)
		return
	}

	msgEvt := event.New(event.TypeWorkerInput, w.ID(), map[string]any{"text": text})
	msgEvt.TraceID = w.CurrentTraceID()
	_ = w.Channel.Send(context.Background(), msgEvt, target)

	w.ReplyCompleted(callerID, callID, toolName, fmt.Sprintf("message sent to %s", target), traceID)
}

// handleListWorkers serves the list_workers tool: it returns all known workers
// with their tools and published events (grouped by provider via
// DiscoveredWorkers) and triggers a worker.discover to refresh the cache for the
// next call.
func handleListWorkers(w *reasonBase.BaseReasonWorker, callID, toolName, callerID, traceID string, args map[string]any) {
	// Trigger re-discovery so the next call gets fresh data.
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), nil))

	snapshot := w.DiscoveredWorkers()
	b, err := json.Marshal(snapshot)
	if err != nil {
		w.ReplyFailed(callerID, callID, toolName, fmt.Sprintf(
			"list_workers could not serialize the worker list: a worker's announced tool/event carried "+
				"a field that cannot be serialized (%v). This usually means a worker.ready declared an invalid "+
				"schema. Ask that worker to fix its declaration, then retry.", err), traceID)
		return
	}

	w.ReplyCompleted(callerID, callID, toolName, string(b), traceID)
	log.Printf("[reason %s] list_workers → %d workers", w.ID(), len(snapshot))
}

// handleContextOp responds to a worker.update op=context.compress/rotate
// request: it is the default worker's context strategy. It shrinks the
// transcript itself (compactTranscript) and, because the mechanism fired the
// event but cannot observe this async work, books the completion here: it
// broadcasts worker.updated done and schedules the next round via TryReason.
// overrideDirective is the program-provided summarizer prompt (empty for the
// built-in fallback); the requester may additionally carry a directive (compress
// focus) or a carry (rotate) in the payload, and both are appended to the
// resolved compaction directive.
func handleContextOp(w *reasonBase.BaseReasonWorker, evt event.Event, overrideDirective string) {
	op, _ := evt.Payload["op"].(string)
	traceID := evt.TraceID
	isRotate := op == "context.rotate"

	directive := overrideDirective
	if directive == "" {
		directive = FallbackCompactDirective
	}
	if extra, _ := evt.Payload["directive"].(string); !isRotate && extra != "" {
		directive = directive + "\nCaller focus: " + extra
	}
	if carry, _ := evt.Payload["carry"].(string); isRotate && carry != "" {
		directive = directive + "\nCarry into the new episode: " + carry
	}

	go func() {
		err := compactTranscript(w, context.Background(), isRotate, directive)
		log.Printf("[reason %s] meta op %s done: %v", w.ID(), op, err)
		done := event.New(event.TypeWorkerUpdated, w.ID(), map[string]any{
			"op": op, "done": true, "error": fmt.Sprintf("%v", err),
		})
		done.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), done)
		w.TryReason(context.Background())
	}()
}
