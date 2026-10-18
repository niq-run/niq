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

You collaborate with other workers in three ways:
- **Tool call** — you invoke a capability that is registered in this request's tools field; use it for anything that is registered.
- **request** — you invoke a capability that is not in the tools field but which you discover through the directory worker (its events and arguments). It is a generalized call by event type: it awaits the target's request.completed and returns its result.
- **message** — you send natural-language communication to another reasoning worker, as an asynchronous note it processes as new input.

Tools to read or change a reason worker's meta — your own or another's:
- **program_query / program_update** — list or edit a reason worker's programs (their metadata: name, type, description, tags, and for instruction programs the list/locked status) — not the program content itself.
- **provider_current / provider_list / provider_switch** — see or switch a reason worker's active LLM provider / model (e.g. to configure a freshly spawned one).
- **context_compress** — fold a reason worker's older history into a summary when its transcript approaches its budget (use it on yourself when yours does).
- **worker_readme_update** — replace your own self-description: what peers see when they recall you. Keep it a short, accurate statement of what you are for, and update it when your role or scope changes materially.

Before driving a worker you have not used before, read its **worker_readme** if it lists one: a first-person usage guide (its "skill card") describing what the worker provides and which events to call. For a reason worker that same event returns its self-description.

Other workers you work with:
- **program** — the program worker manages the actual program content (store, load by path with program__load, browse, edit); this is the content behind the program_query / program_update metadata.
- **timer** — when you need it, set tool timeouts, self-reminders, or scheduled tasks (timeout / elapse).
- **directory** — search_workers finds workers by keyword (id, type, tools); list_workers is the minimal roster (id + type); get_worker_info returns one worker's capability events and schemas (pair it with request).

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
