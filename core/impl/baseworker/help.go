package baseworker

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/niq-run/niq/core/itfs/event"
)

// HelpFunc renders a custom guide for one event, like a CLI's per-subcommand
// `-h`. params is the request's selector (e.g. {"worker": "reason"} for spawn).
// ok=false means "no custom guide matched these params": the generic guide built
// from the event's declared contract is returned instead.
type HelpFunc func(params map[string]any) (text string, ok bool)

// RegisterHelp registers the worker.help capability: a directed worker.help
// call is answered with help for ONE of this worker's events.
//
// The request shape is:
//
//	{"event_name": "spawn", "params": {"worker": "reason"}}
//
// event_name names an event this worker serves (any registered extension);
// params is an optional selector that a worker-specific HelpFunc may use to
// return a more precise guide. Omitting event_name returns an index of every
// event this worker serves.
//
// guides is keyed by event type and may be nil — without a custom guide every
// event still gets a generic guide rendered from its declared description,
// parameters and response. A guide wins only when it returns ok=true; a
// selector it does not recognize falls back to the generic guide.
//
// Like Register, registering this capability is safe at any time, including from
// within a handler. The guides map itself is read-only after registration;
// call RegisterHelp again to replace the whole capability.
func (w *BaseWorker) RegisterHelp(guides map[event.EventType]HelpFunc) {
	w.Register(Extension{
		Event:    event.TypeWorkerHelp,
		Response: StringSchema("the requested help text (markdown)"),
		Description: "Read help for one of this worker's events, like a CLI's -h. Pass event_name to explain " +
			"that event; params may select a more specific guide (e.g. {\"worker\": \"reason\"} for spawn). " +
			"Omit event_name to list every event this worker serves.",
		Parameters: ObjectSchema(map[string]any{
			"event_name": map[string]any{
				"type":        "string",
				"description": "Event to explain (e.g. spawn). Empty lists all served events.",
			},
			"params": map[string]any{
				"type":        "object",
				"description": "Optional selector for an event-specific guide, e.g. {\"worker\": \"reason\"}.",
			},
		}),
	}, func(evt event.Event) {
		tc := ParseToolCall(evt)
		if tc.CallID == "" {
			return
		}
		text, err := w.helpText(evt, tc.Args, guides)
		if err != nil {
			w.ReplyFailed(tc.CallerID, tc.CallID, err.Error(), tc.TraceID)
			return
		}
		w.ReplyCompleted(tc.CallerID, tc.CallID, text, tc.TraceID)
	})
}

// helpText selects the guide for one worker.help request. Internal extensions
// are only visible to their owner, mirroring DispatchExtension's scope rule.
func (w *BaseWorker) helpText(evt event.Event, args map[string]any, guides map[event.EventType]HelpFunc) (string, error) {
	includeInternal := evt.WorkerId == w.ID()
	exts := w.Extensions()

	name := strings.TrimSpace(ArgString(args, "event_name"))
	if name == "" {
		return helpIndex(w.ID(), exts, includeInternal), nil
	}

	params, _ := args["params"].(map[string]any)

	for _, ext := range exts {
		if string(ext.Event) != name {
			continue
		}
		if ext.Internal && !includeInternal {
			return "", fmt.Errorf("worker.help: %s does not expose event %q", w.ID(), name)
		}
		if g := guides[ext.Event]; g != nil {
			if text, ok := g(params); ok {
				return text, nil
			}
		}
		return eventHelp(ext, len(params) > 0), nil
	}

	return "", fmt.Errorf("worker.help: %s does not serve event %q; served events: %s",
		w.ID(), name, strings.Join(visibleEventNames(exts, includeInternal), ", "))
}

// helpIndex renders the worker's capability list, the "no arguments" half of
// worker.help.
func helpIndex(id string, exts []Extension, includeInternal bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — help\n\n", id)
	b.WriteString("Call any event below by sending it to this worker with a request id; " +
		"the reply arrives as `request.completed`.\n\n")
	for _, ext := range visibleExtensions(exts, includeInternal) {
		desc := summary(ext.Description)
		if desc == "" {
			desc = "(no description)"
		}
		fmt.Fprintf(&b, "- `%s` — %s\n", ext.Event, desc)
	}
	b.WriteString("\nAsk for one event with `{\"event_name\": \"<event>\"}`. " +
		"Some events also accept `params` to pick a more specific guide.\n")
	return b.String()
}

// eventHelp is the generic guide for one event, rendered from its declared
// contract. It is what a worker gets without writing any help text, and the
// fallback for params a custom HelpFunc does not recognize.
func eventHelp(ext Extension, paramsGiven bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", ext.Event)
	if desc := strings.TrimSpace(ext.Description); desc != "" {
		b.WriteString(desc)
		b.WriteString("\n\n")
	}
	if len(ext.Parameters) > 0 {
		b.WriteString("## Arguments\n\n```json\n")
		b.WriteString(prettyJSON(ext.Parameters))
		b.WriteString("\n```\n\n")
	}
	if len(ext.Response) > 0 {
		b.WriteString("## Reply\n\n```json\n")
		b.WriteString(prettyJSON(ext.Response))
		b.WriteString("\n```\n\n")
	}
	b.WriteString("## How to call\n\nSend this event to the worker as a request: the payload IS the " +
		"arguments object above, and the reply echoes the same request id as `request.completed`.\n")
	if paramsGiven {
		b.WriteString("\nNo event-specific guide matched `params`; this is the declared contract.\n")
	}
	return b.String()
}

// visibleExtensions returns the extensions a caller may see, sorted by event
// type so the help index is stable across registrations.
func visibleExtensions(exts []Extension, includeInternal bool) []Extension {
	out := make([]Extension, 0, len(exts))
	for _, ext := range exts {
		if ext.Internal && !includeInternal {
			continue
		}
		out = append(out, ext)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Event < out[j].Event })
	return out
}

func visibleEventNames(exts []Extension, includeInternal bool) []string {
	visible := visibleExtensions(exts, includeInternal)
	names := make([]string, 0, len(visible))
	for _, ext := range visible {
		names = append(names, string(ext.Event))
	}
	return names
}

// summary keeps a one-line description one line, so the index stays scannable.
func summary(desc string) string {
	desc = strings.TrimSpace(desc)
	if i := strings.IndexByte(desc, '\n'); i >= 0 {
		desc = desc[:i]
	}
	return desc
}

func prettyJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
