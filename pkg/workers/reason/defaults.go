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
	"github.com/niq-run/niq/core/program"
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
		Event: TypeSendMessage,
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
		handleSendMessage(w, tc.CallID, string(evt.Type), tc.CallerID, tc.TraceID, tc.Args)
	})

	w.Register(baseworker.Extension{
		Event: TypeListWorkers,
		// SelfOnly: list_workers reports this worker's own contract and is not
		// exposed to peers.
		SelfOnly:    true,
		Description: "List all available workers and their capabilities.",
		Parameters:  obj(map[string]any{}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		handleListWorkers(w, tc.CallID, string(evt.Type), tc.CallerID, tc.TraceID, tc.Args)
	})

	w.Register(baseworker.Extension{
		Event: TypeWorkerInfo,
		// SelfOnly: the detail view behind list_workers — this worker's own
		// browsing tool, not a peer-callable contract.
		SelfOnly:    true,
		Description: "Full details for one worker: its tools with complete parameter schemas and its published events.",
		Parameters: obj(map[string]any{
			"worker": map[string]any{"type": "string", "description": "Worker ID, as reported by list_workers"},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		handleWorkerInfo(w, tc.CallID, string(evt.Type), tc.CallerID, tc.TraceID, tc.Args)
	})

	w.Register(baseworker.Extension{
		// The event type comes from the mechanism, so the emitter
		// (emitContextCompress) and this handler cannot drift apart.
		Event:       reasonBase.TypeContextCompress,
		Description: "Compact your own context history: older messages are replaced by a summary, the most recent messages are kept.",
		Parameters: obj(map[string]any{
			"directive": map[string]any{"type": "string",
				"description": "Optional focus for the summary, e.g. what must be preserved."},
		}),
	}, func(evt event.Event) {
		handleContextOp(w, evt, compactDirective)
	})

	w.Register(baseworker.Extension{
		Event:       TypeContextRotate,
		Description: "Rotate your context: summarize the current transcript as a carried digest and start a fresh context.",
		Parameters: obj(map[string]any{
			"carry": map[string]any{"type": "string",
				"description": "Optional instruction for what to carry into the digest."},
		}),
	}, func(evt event.Event) {
		handleContextOp(w, evt, compactDirective)
	})

	// The context ops are self-editing: a call rewrites this worker's own
	// transcript, so the mechanism excludes them from it (see
	// BaseReasonWorker.RegisterTranscriptEditEvent).
	w.RegisterTranscriptEditEvent(reasonBase.TypeContextCompress)
	w.RegisterTranscriptEditEvent(TypeContextRotate)

	// Program management: query or mutate this worker's instruction/playbook
	// list. Deliberately NOT SelfOnly — the list is meant to be edited by other
	// authorized workers (e.g. webui-hiw); bus permissions gate who may send
	// the directed events. These are ordinary meta-extensions (no transcript
	// edit), so handleToolCalls dispatches them normally and the
	// request.completed reply schedules the next round.
	w.Register(baseworker.Extension{
		Event:       TypeProgramQuery,
		Description: "List this worker's instruction and playbook programs (name, type, description, tags, locked).",
		Parameters:  obj(map[string]any{}),
	}, func(evt event.Event) {
		handleProgramQuery(w, evt)
	})

	w.Register(baseworker.Extension{
		Event: TypeProgramUpdate,
		Description: "Add or remove one of this worker's programs. This manages only the metadata reference registered " +
			"on THIS worker: a program's actual content lives in the program worker, so it must already exist there — " +
			"if it does not, create it in the program worker first (e.g. via its register tool), then add the reference here. " +
			"Instructions are inlined into the system prompt; playbooks are referenced by metadata only (name/description/tags).",
		Parameters: obj(map[string]any{
			"op": map[string]any{"type": "string", "enum": []string{"add", "remove"},
				"description": "add a new program, or remove an existing one by name"},
			"name":        map[string]any{"type": "string", "description": "program name (required for both ops)"},
			"content_type": map[string]any{"type": "string", "enum": []string{"instruction", "playbook"},
				"description": "required for add: instruction is inlined, playbook is a metadata reference"},
			"content": map[string]any{"type": "string",
				"description": "instruction text; required for content_type=instruction, ignored for playbook"},
			"description": map[string]any{"type": "string", "description": "human description (playbooks surface this in the prompt)"},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"locked":      map[string]any{"type": "boolean", "description": "if true, the program cannot be removed via this tool"},
		}),
	}, func(evt event.Event) {
		handleProgramUpdate(w, evt)
	})
}

// TypeContextRotate is the default worker's context-rotate event — its own
// extra beyond the mechanism's context.compress convention, so it lives here.
const TypeContextRotate event.EventType = "context.rotate"

// The default worker's own LLM-facing tools, each its own event type (see the
// request-response convention in pkg/reason).
const (
	TypeSendMessage event.EventType = "send_message"
	TypeListWorkers event.EventType = "list_workers"
	TypeWorkerInfo  event.EventType = "get_worker_info"
)

// TypeProgramQuery / TypeProgramUpdate are the program-management extensions:
// read this worker's instruction/playbook list, or add/remove one entry. They
// are NOT SelfOnly so other authorized workers (e.g. webui-hiw) can manage
// them; bus permissions control who may address the directed events.
const (
	TypeProgramQuery  event.EventType = "program.query"
	TypeProgramUpdate event.EventType = "program.update"
)

// handleSendMessage serves the send_message tool: forwards the text to the
// target worker as a worker.input event and replies with a tool result.
func handleSendMessage(w *reasonBase.BaseReasonWorker, callID, toolName, callerID, traceID string, args map[string]any) {
	target, _ := args["target"].(string)
	text, _ := args["text"].(string)
	if target == "" || text == "" {
		w.ReplyFailed(callerID, callID, "target and text are required", traceID)
		return
	}

	msgEvt := event.New(event.TypeWorkerInput, w.ID(), map[string]any{"text": text})
	msgEvt.TraceID = w.CurrentTraceID()
	_ = w.Channel.Send(context.Background(), msgEvt, target)

	w.ReplyCompleted(callerID, callID, fmt.Sprintf("message sent to %s", target), traceID)
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
		w.ReplyFailed(callerID, callID, fmt.Sprintf(
			"list_workers could not serialize the worker list: a worker's announced tool/event carried "+
				"a field that cannot be serialized (%v). This usually means a worker.ready declared an invalid "+
				"schema. Ask that worker to fix its declaration, then retry.", err), traceID)
		return
	}

	w.ReplyCompleted(callerID, callID, string(b), traceID)
	log.Printf("[reason %s] list_workers → %d workers", w.ID(), len(snapshot))
}

// handleWorkerInfo serves the get_worker_info tool: the detail view for one
// worker, with complete tool parameter schemas — the part list_workers
// deliberately omits to stay small. Unknown workers fail with the list of
// known ids so the model can self-correct.
func handleWorkerInfo(w *reasonBase.BaseReasonWorker, callID, toolName, callerID, traceID string, args map[string]any) {
	target, _ := args["worker"].(string)
	if target == "" {
		w.ReplyFailed(callerID, callID, "get_worker_info requires the 'worker' parameter (target worker ID)", traceID)
		return
	}
	// Fresh data, same as list_workers: ask everyone to re-announce first.
	_ = w.Channel.Broadcast(context.Background(), event.New(event.TypeWorkerDiscover, w.ID(), nil))

	info, ok := w.WorkerInfo(target)
	if !ok {
		w.ReplyFailed(callerID, callID, fmt.Sprintf(
			"unknown worker %q — call list_workers to see the known ids", target), traceID)
		return
	}
	b, err := json.Marshal(info)
	if err != nil {
		w.ReplyFailed(callerID, callID, fmt.Sprintf("get_worker_info could not serialize %q: %v", target, err), traceID)
		return
	}
	w.ReplyCompleted(callerID, callID, string(b), traceID)
	log.Printf("[reason %s] get_worker_info → %s (%d tools)", w.ID(), target, len(info.Tools))
}

// handleContextOp responds to a context.compress / context.rotate request: it
// is the default worker's context strategy. It shrinks the transcript itself
// (compactTranscript) and, because the mechanism fired the event but cannot
// observe this async work, books the completion here: it answers with
// request.completed / request.failed echoing the request's id and schedules
// the next round via TryReason. overrideDirective is the program-provided
// summarizer prompt (empty for the built-in fallback); the requester may
// additionally carry a directive (compress focus) or a carry (rotate) in the
// payload, and both are appended to the resolved compaction directive.
func handleContextOp(w *reasonBase.BaseReasonWorker, evt event.Event, overrideDirective string) {
	isRotate := evt.Type == TypeContextRotate
	traceID := evt.TraceID

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
		log.Printf("[reason %s] context op %s done: %v", w.ID(), evt.Type, err)
		typ := event.TypeRequestCompleted
		payload := map[string]any{"error": fmt.Sprintf("%v", err)}
		if err != nil {
			typ = event.TypeRequestFailed
		}
		done := event.New(typ, w.ID(), payload)
		done.RequestId = evt.RequestId
		done.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), done)
		w.TryReason(context.Background())
	}()
}

// handleProgramQuery serves the program.query tool: it returns the worker's
// current instruction/playbook list as a request.completed whose "result" is
// the JSON-encoded list. The next reasoning round re-renders the system prompt
// from this same list via buildInstruction.
//
// Extension handlers are invoked from process(), which holds w.mu for the
// whole dispatch — use the *Locked accessors and never take w.mu here
// (sync.Mutex is not reentrant; locking would self-deadlock the event loop).
func handleProgramQuery(w *reasonBase.BaseReasonWorker, evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	b, err := json.Marshal(w.ProgramsLocked())
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID,
			fmt.Sprintf("program.query could not serialize the program list: %v", err), tc.TraceID)
		return
	}
	w.ReplyCompleted(tc.CallerID, tc.CallID, string(b), tc.TraceID)
}

// handleProgramUpdate serves the program.update tool: add or remove a single
// program entry. "add" appends a new program (error if the name exists);
// "remove" deletes by name (error if absent or Locked). On success it raises
// the durable-change signal so the worker snapshots the new list, and replies
// with request.completed / request.failed echoing the request's id — the
// normal reply path schedules the next reasoning round.
//
// Runs inside process() with w.mu held (see handleProgramQuery).
func handleProgramUpdate(w *reasonBase.BaseReasonWorker, evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	op, _ := tc.Args["op"].(string)

	var result string
	var err error
	switch op {
	case "add":
		var p program.Program
		p, err = programFromArgs(tc.Args)
		if err == nil {
			err = w.AddProgramLocked(p)
		}
		if err == nil {
			result = "added program " + p.Name
		}
	case "remove":
		name, _ := tc.Args["name"].(string)
		if name == "" {
			err = fmt.Errorf("name is required for remove")
		} else {
			err = w.RemoveProgramLocked(name)
		}
		if err == nil {
			result = "removed program " + name
		}
	default:
		err = fmt.Errorf("unknown op %q (want add or remove)", op)
	}

	if err == nil {
		w.NotifyDurableChange()
		w.ReplyCompleted(tc.CallerID, tc.CallID, result, tc.TraceID)
		return
	}
	w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
}

// programFromArgs builds a program.Program from a tool-call argument map. The
// convention mirrors program.Meta / ProgramContent: name and content_type are
// required; content_type=instruction requires content (it is inlined into the
// prompt), while playbook is a metadata reference and ignores content.
func programFromArgs(args map[string]any) (program.Program, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return program.Program{}, fmt.Errorf("name is required")
	}
	ct, _ := args["content_type"].(string)
	if ct != "instruction" && ct != "playbook" {
		return program.Program{}, fmt.Errorf("content_type must be 'instruction' or 'playbook'")
	}
	content, _ := args["content"].(string)
	if ct == "instruction" && content == "" {
		return program.Program{}, fmt.Errorf("content is required for content_type=instruction")
	}
	desc, _ := args["description"].(string)
	locked, _ := args["locked"].(bool)
	return program.Program{
		Meta: program.Meta{
			Name:        name,
			ContentType: program.ContentType(ct),
			Description: desc,
			Tags:        toStringSlice(args["tags"]),
			Locked:      locked,
		},
		EntryContent: program.ProgramContent{
			FormType: program.FormTypePrompt,
			Content:  content,
		},
	}, nil
}

// toStringSlice coerces a JSON-decoded []any into []string, dropping non-string
// entries. A nil/absent value yields nil.
func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
