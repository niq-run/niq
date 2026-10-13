// system prompt template.
//
// The system prompt is built from a fixed template and the worker's loaded
// Programs. Instruction Programs contribute their full content; Playbook
// Programs contribute only their metadata (name, path, description, tags) —
// the path is what the LLM passes to program__load to pull the content.
//
// The template is compiled once at package init and executed on each
// reasoning round with the current program data.
package reason

import (
	"strings"
	"text/template"
)

// systemPromptTmpl is the template for the Reason Worker's system prompt.
// The opening lines set the worker's identity and how it collaborates over the
// event bus; {{.WorkerID}} is replaced with the worker's ID.
// Playbooks contribute only metadata (name, path, description, tags); their
// path is what loads the content.
// Instructions contribute their full entry content.
// Locked programs are marked with [locked] so the LLM knows they are
// immutable system-level rules that cannot be modified via meta-extensions.
const systemPromptText = `You are a reasoning worker inside the niq system, your ID is {{.WorkerID}}.

Workers in the niq system collaborate by exchanging events over the bus. Tool workers cover specific domains and capabilities; reason workers like you keep reasoning and sending/processing events around their own focus and context.

You collaborate with other workers over events in three ways:
- a **tool call** invokes a capability already declared in this request's tools field (the default, preferred path);
- **request** invokes a capability on another worker by event type when it is not a declared tool — learn the target's events with the directory worker's get_worker_info, then call request with event and arguments, and it waits for that worker's request.completed and returns its result;
- **message** is your channel for natural-language communication with other reasoning workers whenever you need to talk to one (it sends an asynchronous note they process as new input; it does not return a structured result).

A few tools matter most to you:
- **context_compress** — fold older history into a summary when the transcript approaches its budget.
- **program_query / program_update** — inspect or edit your own instruction / playbook programs (the rules you must follow).
- **provider_current / provider_list / provider_switch** — see or switch your active LLM provider / model.
- **timer** — when you need it, the timer worker lets you set tool-timeouts, self-reminders, and scheduled tasks (timeout / timer.reminder).
- **directory** — list_workers gives the fleet roster; get_worker_info returns one worker's capability events and schemas (pair it with request).

The tools available to you are declared in this request's tools field, with their parameters. Prefer a worker's dedicated tool for its own domain; fall back to request only when the capability is not exposed.

{{if .Playbooks}}## Available Playbooks
Reference procedures you may choose to follow when they fit the task. Only their
metadata is listed here — load a playbook's content with program__load, passing
its path.
{{range .Playbooks}}- {{.Name}} (path: {{.Path}}): {{.Description}} (tags: {{.Tags}})
{{end}}
{{end}}{{if .Instructions}}## Instructions
Rules you must follow — [locked] ones are immutable.
{{range .Instructions}}{{if .Locked}}[locked] {{end}}{{.Content}}

{{end}}{{end}}`

// templateData is the data passed to the system prompt template.
type templateData struct {
	WorkerID     string
	Playbooks    []playbookEntry
	Instructions []instructionEntry
}

type playbookEntry struct {
	Name        string
	Path        string
	Description string
	Tags        string
}

type instructionEntry struct {
	Content string
	Locked  bool
}

// systemPromptTmpl is the compiled template, initialized at startup.
var systemPromptTmpl = template.Must(
	template.New("system_prompt").
		Funcs(template.FuncMap{
			"formatTags": func(tags []string) string {
				return strings.Join(tags, ", ")
			},
		}).
		Parse(systemPromptText),
)

// buildInstruction executes the system prompt template with the worker's
// current programs. Instruction Programs provide full content; Playbook
// Programs provide only metadata plus the path that addresses their content.
func (w *BaseReasonWorker) buildInstruction() string {
	var data templateData
	data.WorkerID = w.ID()

	for _, p := range w.programs {
		switch p.ContentType {
		case "instruction":
			if p.EntryContent.Content != "" {
				data.Instructions = append(data.Instructions, instructionEntry{
					Content: p.EntryContent.Content,
					Locked:  p.Locked,
				})
			}
		case "playbook":
			// Path is the only address the program worker accepts, so it is
			// what the LLM must hand to program__load. Fall back to the name
			// for programs seeded without one — the two are the same by
			// convention.
			path := p.Path
			if path == "" {
				path = p.Name
			}
			data.Playbooks = append(data.Playbooks, playbookEntry{
				Name:        p.Name,
				Path:        path,
				Description: p.Description,
				Tags:        strings.Join(p.Tags, ", "),
			})
		}
	}

	var buf strings.Builder
	if err := systemPromptTmpl.Execute(&buf, data); err != nil {
		// Template is fixed at compile time — execution should never fail.
		return ""
	}
	return buf.String()
}
