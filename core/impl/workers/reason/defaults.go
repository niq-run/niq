// The default reason worker's extension toolkit: the generic tools
// (send_message / request) and the context meta ops (compress / rotate).
// These are deliberately outside pkg/reason — they are the default worker's
// own choice of tools, not part of the shared reasoning mechanism. They are
// implemented entirely through the mechanism's exported accessors
// (ReplyCompleted / ReplyFailed / CurrentTraceID / Transcript / TryReason /
// Channel), so nothing here reaches into unexported
// state. The
// context.compress / context.rotate handlers are this worker's own context
// strategy: they edit the transcript directly and book their own completion.
package reason

import (
	"context"
	"fmt"
	"log"

	"github.com/niq-run/niq/core/impl/baseworker"
	reasonBase "github.com/niq-run/niq/core/impl/reason"
	"github.com/niq-run/niq/core/impl/reason/transcript"
	"github.com/niq-run/niq/core/itfs/event"
	"github.com/niq-run/niq/core/itfs/llm"
	"github.com/niq-run/niq/core/itfs/program"
)

// registerDefaultExtensions registers the toolkit the generic reason worker
// adds on top of the base extensions (provider.switch / provider.list /
// provider.current): send_message, request, context.compress,
// context.rotate. A reason-family worker that wants a different toolkit simply
// does not call this. compactDirective is the program-provided summarizer
// override (empty for the built-in fallback); it is owned by this worker, not
// the mechanism, and captured here so the context strategy can resolve it.
// RegisterDefaultExtensions registers the default reason-family toolkit on w:
// message / request / context.compress / program.query / program.attach /
// program.detach. The
// generic reason worker uses it via NewWorker; a PARALLEL reason-family worker
// (e.g. the coding worker) reuses the same baseline while adding its own
// differences (tool-list policy, compaction directive). compactDirective is the
// summarizer override (empty for the built-in fallback).
func RegisterDefaultExtensions(w *reasonBase.BaseReasonWorker, compactDirective string) {
	registerDefaultExtensions(w, compactDirective)
}

func registerDefaultExtensions(w *reasonBase.BaseReasonWorker, compactDirective string) {
	obj := func(props map[string]any) map[string]any {
		return map[string]any{"type": "object", "properties": props}
	}

	w.Register(baseworker.Extension{
		Event: reasonBase.TypeMessage,
		// Internal: a worker's own addressed-messaging tool is not something
		// peers should be able to call, so it is announced only to itself.
		Internal:    true,
		Response:    baseworker.StringSchema("confirmation: \"message sent to <worker>\"; the target's answer, if any, arrives later as worker.input"),
		Description: "Send a message to a specific worker on the bus — this is how you talk to another reasoning worker, both to start a conversation and to reply to one. The message is delivered in append mode: it does not interrupt the target's in-flight reasoning — it is appended as a supplement and the target schedules a fresh round.",
		Parameters: obj(map[string]any{
			"target": map[string]any{"type": "string", "description": "Target worker ID"},
			"text":   map[string]any{"type": "string", "description": "Message text"},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		handleSendMessage(w, tc.CallID, string(evt.Type), tc.CallerID, tc.TraceID, tc.Args)
	})

	w.Register(baseworker.Extension{
		Event: reasonBase.TypeRequest,
		// Internal: this is the worker's own directed-call escape hatch — peers
		// call it here only indirectly (a peer reason invokes its own request).
		// The actual forwarding is done in handleToolCalls (pkg/reason): the
		// peer capability event goes out under THIS call's id, so no second
		// placeholder is needed. This handler is defensive only (normally never
		// reached).
		Internal:    true,
		Response:    baseworker.ObjectSchema(map[string]any{"result": map[string]any{"type": "string", "description": "the target capability's reply text — its shape is whatever that capability declares as its response"}}),
		Description: "Invoke a capability on another worker without it being a listed tool: provide target (worker id), event (the capability event type, as shown by the directory worker's describe), and arguments. The call awaits the worker's request.completed and returns its result. Not limited to capabilities discovered by this worker — the target may serve events that were never announced here (e.g. a peer reason worker's worker.readme).",
		Parameters: obj(map[string]any{
			"target":    map[string]any{"type": "string", "description": "Target worker ID"},
			"event":     map[string]any{"type": "string", "description": "Capability event type on the target worker"},
			"arguments": map[string]any{"type": "object", "description": "Capability arguments"},
		}),
	}, func(evt event.Event) {
		// Defensive: handleToolCalls pre-expands request into a real peer call,
		// so this should not be dispatched. If it somehow is, fail loudly rather
		// than leaving the call unresolved.
		tc := baseworker.ParseToolCall(evt)
		w.ReplyFailed(tc.CallerID, tc.CallID, "request must be dispatched via the forward path", tc.TraceID)
	})

	// run: the third built-in send, alongside message and request — it executes
	// a script program. handleToolCalls fills in the target (the program worker)
	// and the event, so this registration only declares the contract the model
	// sees; the handler is defensive for the same reason request's is.
	w.Register(baseworker.Extension{
		Event:    TypeRunProgram,
		Internal: true, // the worker's own tool: not something peers call
		Response: baseworker.ObjectSchema(map[string]any{
			"output":        map[string]any{"description": "the script's return value (sdk.result or a top-level return), any JSON type"},
			"log":           baseworker.ArraySchema(map[string]any{"type": "string"}),
			"log_truncated": map[string]any{"type": "number", "description": "how many earlier log lines were dropped"},
		}),
		Description: "Run a JavaScript program on the program worker: a stored script (path) or inline source. Inside it, " +
			"await sdk.call(target, event, arguments) sends the same event a tool call or request would and resolves with " +
			"its reply value; await sdk.message(target, text) leaves a note without waiting; sdk.result(payload) sets the " +
			"output; sdk.log(text) comes back with it. Use it to send a whole " +
			"sequence of events in one step instead of one reasoning round per call. The sandbox has no filesystem, " +
			"process or network access — everything goes through sdk.call.",
		Parameters: obj(map[string]any{
			"path": map[string]any{"type": "string",
				"description": "Run a stored script program by its content path (its name for the entry content). Provide this or source."},
			"source": map[string]any{"type": "string",
				"description": "Run inline JavaScript (nothing persisted). Provide this or path."},
			"args": map[string]any{"type": "object",
				"description": "Arguments handed to the script as sdk.args (default {})."},
			"timeout_seconds": map[string]any{"type": "number",
				"description": "Wall-clock limit for the whole run in seconds (default 120), covering nested calls."},
		}),
	}, func(evt event.Event) {
		tc := baseworker.ParseToolCall(evt)
		w.ReplyFailed(tc.CallerID, tc.CallID, "run must be dispatched via the forward path", tc.TraceID)
	})

	w.Register(baseworker.Extension{
		// The event type comes from the mechanism, so the emitter
		// (invokeContextCompress) and this handler cannot drift apart.
		Event:       reasonBase.TypeContextCompress,
		Response:    baseworker.StringSchema("how much was compacted"),
		Description: "Compact your own context history: older messages are replaced by a summary, the most recent messages are kept.",
		Parameters: obj(map[string]any{
			"directive": map[string]any{"type": "string",
				"description": "Optional focus for the summary, e.g. what must be preserved."},
		}),
	}, func(evt event.Event) {
		handleContextOp(w, evt, compactDirective)
	})

	// context.rotate is TEMPORARILY DISABLED (backing off its design while the
	// context strategy settles; the handler + TypeContextRotate stay for later
	// re-enable). Re-register to expose the tool again.
	/*
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
	*/

	// Program management: query or mutate this worker's instruction/playbook
	// list. Deliberately advertised (not Internal): the list is meant to be
	// edited by other authorized workers (e.g. webui-hiw); bus permissions gate
	// who may send the directed events. These are ordinary meta-extensions (no
	// transcript edit), so handleToolCalls dispatches them normally and the
	// request.completed reply schedules the next round.
	w.Register(baseworker.Extension{
		Event:    TypeProgramQuery,
		Response: baseworker.ArraySchema(baseworker.ObjectSchema(map[string]any{"name": map[string]any{"type": "string"}, "content_type": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "tags": baseworker.ArraySchema(map[string]any{"type": "string"}), "locked": map[string]any{"type": "boolean"}, "path": map[string]any{"type": "string"}})),
		Description: "List this worker's instruction and playbook programs (name, type, description, tags, locked, path). " +
			"Instructions carry their content here; playbooks are references — only metadata is listed, and their content " +
			"is read from the program worker by passing the path to program__read.",
		Parameters: obj(map[string]any{}),
	}, func(evt event.Event) {
		handleProgramQuery(w, evt)
	})

	w.Register(baseworker.Extension{
		Event:    TypeProgramAttach,
		Response: baseworker.StringSchema("confirmation naming the attached program"),
		Description: "Attach a program to this worker. Instruction: supply content and it is inlined into the " +
			"system prompt — self-contained, nothing to set up elsewhere. Playbook: only name/description/tags are " +
			"kept here (surfaced in the prompt); its content lives in the program worker, so create it there first " +
			"(touch then write) and read it with program__read(\"<name>\"). Attaching a name that is already mounted " +
			"fails — detach it first to replace it.",
		Parameters: obj(map[string]any{
			"name": map[string]any{"type": "string", "description": "program name — also its root path"},
			"content_type": map[string]any{"type": "string", "enum": []string{"instruction", "playbook"},
				"description": "instruction is inlined into the prompt; playbook is a metadata reference"},
			"content": map[string]any{"type": "string",
				"description": "instruction text; required for content_type=instruction, ignored for playbook"},
			"description": map[string]any{"type": "string", "description": "human description (playbooks surface this in the prompt)"},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"locked":      map[string]any{"type": "boolean", "description": "if true, the program cannot be detached via this tool"},
		}),
	}, func(evt event.Event) {
		handleProgramAttach(w, evt)
	})

	w.Register(baseworker.Extension{
		Event:       TypeProgramDetach,
		Response:    baseworker.StringSchema("confirmation naming the detached program"),
		Description: "Detach a program from this worker by name. The program itself is not deleted — only this worker's mounting of it. Locked programs cannot be detached.",
		Parameters: obj(map[string]any{
			"name": map[string]any{"type": "string", "description": "program name to unmount"},
		}),
	}, func(evt event.Event) {
		handleProgramDetach(w, evt)
	})
}

// TypeContextRotate is the default worker's context-rotate event — its own
// extra beyond the mechanism's context.compress convention, so it lives here.
const TypeContextRotate event.EventType = "context.rotate"

// The default worker's own LLM-facing tools, each its own event type (see the
// request-response convention in pkg/reason). The fleet roster deliberately
// lives on the directory worker, not here: a reason worker must not treat peer
// reasons' ready events as callable tools, and discovery is the directory's
// service.
// TypeMessage / TypeRequest are the default worker's generated self tools:
// message (fire-and-forget worker.input) and request (forward a peer capability
// call and await its request.completed via the normal tracker).
const (
	TypeMessage    event.EventType = reasonBase.TypeMessage
	TypeRequest    event.EventType = reasonBase.TypeRequest
	TypeRunProgram event.EventType = reasonBase.TypeRunProgram
)

// TypeProgramQuery / TypeProgramAttach / TypeProgramDetach are the program
// management extensions: read this worker's instruction/playbook list, mount one
// entry on it, or unmount one. They are advertised (not Internal) so other
// authorized workers (e.g. webui-hiw) can manage them; bus permissions control
// who may address the directed events.
//
// attach/detach are two events rather than one "update" with an op argument:
// the operation IS the event, which is the same shape every other capability
// uses and leaves no discriminated-union argument to misread.
const (
	TypeProgramQuery  event.EventType = "program.query"
	TypeProgramAttach event.EventType = "program.attach"
	TypeProgramDetach event.EventType = "program.detach"
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
	if !w.MayAddress(target) {
		w.ReplyFailed(callerID, callID,
			fmt.Sprintf("%s is outside this worker's collaboration scope", target), traceID)
		return
	}

	msgEvt := event.New(event.TypeWorkerInput, w.ID(), map[string]any{
		// Default to append mode: a cross-worker message supplements the target's
		// ongoing thought and schedules a fresh round without interrupting its
		// in-flight reasoning call. This is deliberate for machine-to-machine
		// chatter — only a human (webui) or an explicit interrupt should yank a
		// worker mid-thought. The target's handlerInput reads input_mode and falls
		// back to interrupt ("default") if absent.
		"text":       text,
		"input_mode": "append",
	})
	msgEvt.TraceID = w.CurrentTraceID()
	_ = w.Channel.Send(context.Background(), msgEvt, target)

	w.ReplyCompleted(callerID, callID, fmt.Sprintf("message sent to %s", target), traceID)
}

// handleContextOp responds to a context.compress / context.rotate request: it
// is the default worker's context strategy. It shrinks the transcript itself
// (compactTranscript).
//
// Two paths: when the request carries a tool call id (the model called the
// compress/rotate tool), the call is already tracked as a normal tool with its
// placeholder in the transcript — the replace with the placeholder survives the
// edit because compaction keeps a token-budgeted, pairing-preserved tail —
// so the handler
// replies with a self-directed request.completed echoing that id. That resolves
// the tracked request through the ordinary tool-result pairing (filling the
// placeholder and setting needReason), scheduling the next round naturally.
// When the request has no tool call id (the mechanism auto-fired
// context.compress under hard budget pressure), it does not go through the tool
// machinery at all: it compacts and broadcasts request.completed only for
// observation, leaving the transcript untouched by any tool marker, and
// schedules the next round directly via TryReason.
// overrideDirective is the program-provided summarizer prompt (empty for the
// built-in fallback); the requester may additionally carry a directive (compress
// focus) or a carry (rotate) in the payload, and both are appended to the
// resolved compaction directive.
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

	// The model-invoked path carries the compress/rotate tool call id in
	// RequestId; the auto-fired event (hard budget) has none.
	callID := evt.RequestId

	go func() {
		rewrote, err := compactTranscript(w, context.Background(), isRotate, directive)
		log.Printf("[reason %s] context op %s done: rewrote=%v err=%v", w.ID(), evt.Type, rewrote, err)
		if callID != "" {
			// Model-invoked: resolve the already-tracked request via the normal
			// pairing (fills the placeholder that survived the edit); the
			// resolution's needReason schedules the next round.
			if err != nil {
				w.ReplyFailed(w.ID(), callID, err.Error(), traceID)
				return
			}
			label := "compress"
			if isRotate {
				label = "rotate"
			}
			// Tell the model the real outcome, including how much of the tail budget
			// the transcript currently occupies so it can size tail_tokens sensibly
			// when compression is a no-op.
			usedTokens := w.Transcript().EstimatedTokens()
			w.ReplyCompleted(w.ID(), callID, compactionNote(label, rewrote, usedTokens, w.TailTokens()), traceID)
			return
		}
		// Auto-fired (hard budget): no tool call, so no pairing — broadcast a
		// completion only for observation; the transcript sees no tool marker.
		typ := event.TypeRequestCompleted
		payload := map[string]any{"error": "", "rewrote": rewrote}
		if err != nil {
			typ = event.TypeRequestFailed
			payload["error"] = err.Error()
		}
		// An auto compression is only ever fired when the hard budget threshold was
		// crossed, so it always rewrites the transcript (never a no-op). Record the
		// completion in the transcript as a system note so the model still sees
		// that a compression ran. Only on success: on error the edit was aborted.
		if err == nil && rewrote {
			w.Transcript().Apply(transcript.InputPatch{Messages: []llm.Message{{
				Role:    llm.RoleUser,
				Content: []llm.ContentBlock{{Type: llm.ContentText, Text: autoCompressDoneNote}},
			}}})
		}
		done := event.New(typ, w.ID(), payload)
		done.TraceID = traceID
		_ = w.Channel.Broadcast(context.Background(), done)
		// Only schedule the next round when something actually changed; a no-op
		// compress must not pull the model back for another (fruitless) round.
		reasonForRound := rewrote
		if isRotate {
			reasonForRound = true
		}
		if reasonForRound {
			w.TryReason(context.Background())
		}
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
	// Read-only query: its result is transient, not durable history. The program
	// list travels as structured JSON, not a pre-encoded string.
	w.ReplyCompleted(tc.CallerID, tc.CallID, w.ProgramsLocked(), tc.TraceID)
}

// handleProgramAttach serves program.attach: mount one program on this worker.
// It errors if the name is already mounted (purely additive — detach first to
// replace). On success it raises the durable-change signal so the worker
// snapshots the new list, and replies through the normal path, which schedules
// the next reasoning round.
//
// The two kinds are deliberately asymmetric: an instruction is self-contained
// (its content is supplied here and inlined into the prompt), while a playbook
// is only a reference — name/description/tags live here, and its content stays
// in the program worker to be read on demand. See buildInstruction.
//
// Runs inside process() with w.mu held (see handleProgramQuery).
func handleProgramAttach(w *reasonBase.BaseReasonWorker, evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	p, err := programFromArgs(tc.Args)
	if err == nil {
		err = w.AddProgramLocked(p)
	}
	if err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	w.NotifyDurableChange()
	w.ReplyCompleted(tc.CallerID, tc.CallID, "attached program "+p.Name, tc.TraceID)
}

// handleProgramDetach serves program.detach: unmount one program by name. It
// errors if the program is absent or Locked. The program's content is never
// touched — detaching only removes this worker's mounting of it.
//
// Runs inside process() with w.mu held (see handleProgramQuery).
func handleProgramDetach(w *reasonBase.BaseReasonWorker, evt event.Event) {
	tc := baseworker.ParseToolCall(evt)
	name, _ := tc.Args["name"].(string)
	if name == "" {
		w.ReplyFailed(tc.CallerID, tc.CallID, "name is required", tc.TraceID)
		return
	}
	if err := w.RemoveProgramLocked(name); err != nil {
		w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
		return
	}
	w.NotifyDurableChange()
	w.ReplyCompleted(tc.CallerID, tc.CallID, "detached program "+name, tc.TraceID)
}

// programFromArgs builds a program.Program from a tool-call argument map. The
// convention mirrors program.Meta / ProgramContent: name and content_type are
// required; content_type=instruction requires content (it is inlined into the
// prompt), while playbook is a metadata reference whose content lives in the
// program worker — nothing is stored for it here.
//
// Every program gets Path == its name, so program.query can hand the caller a
// path to feed to the program worker's load tool.
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
	if ct == "playbook" {
		// A playbook is only a reference; its content stays in the program
		// worker and is loaded on demand.
		content = ""
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
		Path: name,
		EntryContent: program.ProgramContent{
			Name:     name,
			FormType: program.FormTypePrompt,
			Path:     name,
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
