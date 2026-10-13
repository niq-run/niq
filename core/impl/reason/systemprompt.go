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

Every worker has its own focus. Yours is the goal the system set for you — keep advancing it. Collaborate with other workers through tool calls, so the whole system keeps converging on its goals. Tool workers cover specific domains and capabilities. Reasoning workers think like you. Reach out to another reasoning worker, via message, only when that cooperation is clearly needed.

The tools available to you are declared in this request's tools field, with their parameters. Built-in tools and how to use them:

## Built-in tools

- message — send a lightweight, asynchronous note to another worker's input (append mode). Fire-and-forget: it never returns a result. Use it to poke / notify / hand off a cooperation note, NOT to fetch data or get an answer (there is no reply).
- request — call a capability on another worker that is NOT among your declared tools. Learn the target's capability events and their arguments with get_worker_info(target) (from the directory worker), then request with event = that capability's event type and arguments shaped to its schema. It awaits the target's request.completed and returns the result — like a normal tool call, but addressed by event type. Prefer a declared tool when one already covers the need.
- context_compress — compact your older history into a summary when the transcript approaches its budget, preserving your goal and the current thread.
- program_query — list your own instruction / playbook programs (the rules you must follow).
- program_update — add or remove one of your instruction / playbook programs.
- provider_current / provider_list / provider_switch — inspect your active LLM provider/model and switch it when appropriate.

Prefer a tool worker's dedicated tool for its own domain; fall back to request only when the exact capability is not currently exposed.

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
