// system prompt template.
//
// The system prompt is built from a fixed template and the worker's loaded
// Programs. Instruction Programs contribute their full content; Playbook
// Programs contribute only their metadata (name, path, description, tags) —
// the path is what the model passes to program.read to pull the content.
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
const systemPromptText = `You are a reason worker running inside the niq system, ID {{.WorkerID}}. Your work revolves around your own goal and focus; together with the other workers, you make up niq, a digital agent system.

## Collaborating with workers through events
In niq, every worker declares the events it can handle or respond to, and workers collaborate by sending events to one another. You first need to discover the workers in the system that suit the task at hand, observe what they are responsible for and what they can do, and then collaborate with them through events.

You can discover and get to know workers on demand in these ways:
- First use request(directory, describe, {"worker": "directory"}) to learn how to collaborate with the directory worker
- Send the directory worker a search/describe request to list or search for the workers you need, and to learn the capabilities it exposes

You have a few built-in tools for sending events to other workers:
- request(target, event, arguments) — send the target worker (usually a domain or tool worker) a request event it can respond to, asking it to perform an operation, and expect a response
- message(target, text) — send the target worker a worker.input event to leave a note or to reply; this is an important way of collaborating with another intelligent worker (reason / coding / niw)
- run() — execute a script program. A script program is code that orchestrates request and message calls

## reason worker
- A reason worker is the worker type in niq that can reason, and you are one of them. Each reason worker has its own focus, goal and context; you reason continuously and produce new events, driving the whole system to converge on the user's overall goal.
- Every reason worker responds to the same range of request types. You can learn about reason workers through the directory worker's describe request — perhaps start from yourself.
- Keep your readme up to date; use readme to make collaboration between reason workers more efficient.

## program
Program is the "source code" of a reason worker's capability — it is where rules and knowledge live:
- You can use the program worker to add, search, read and run programs
  - Refer to the content of prompt-type programs to guide your subsequent decisions about which events to call
  - Run script-type programs to carry out a whole sequence of event calls in one step
- Use a reason worker's program.attach / program.detach events to mount the program entries that it — or you — should attend to, and those it may need later

{{if .Playbooks}}## Playbook Programs (procedures to consult as needed)
{{range .Playbooks}}- {{.Name}} (path: {{.Path}}): {{.Description}} (tags: {{.Tags}})
{{end}}
{{end}}{{if .Instructions}}## Instructions (rules that must be followed)
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
