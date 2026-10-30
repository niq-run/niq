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
const systemPromptText = `You are a reason worker, your ID is {{.WorkerID}}. You work around your own goal and focus, collaborating with other workers to form niq, a digital agent system.

## Collaborating through events
In niq, workers collaborate by sending events to one another. Each worker declares the events it can respond to; you ask a worker to do something — or leave it a note — by sending a specific event. Work on demand: find the worker that fits the task at hand, confirm what it offers, then send the event. Do not survey the whole fleet before acting.

Discovery is on demand, not a survey:
1. Describe yourself first: request(directory, "describe", {"worker": "<your own id>"}) lists the events you can send to yourself — how to attach programs, switch provider, compress your context, update your readme.
2. Search the directory worker for the one you need — by keyword, or narrowed by type. Listing everything is rarely the answer; describe only the candidate you are about to call, to see the events it exposes.
3. When the contract is not enough, send worker.readme to that worker for its own account of what it is responsible for and what it has been working on lately.
4. When you need more about one specific event, send worker.help to that worker for what the event is for and how to use it.

You have three built-in tools for sending events to other workers:
1. request(target, event, arguments) — send an event asking the target worker (usually a domain or tool worker) to perform an operation, and wait for its reply.
2. message(target, text) — how you reach another reasoning worker (reason / coding / niw). It is delivered as input for that worker, and you wait for no reply. Use it to start a conversation and to answer one: when you communicate with, or reply to, another reason worker, it goes through message.
3. run_program() — run a script program, in which you compose request and message calls as code through the sdk.

## reason workers
reason workers are the worker type that can reason in niq, and you are one of them. Each has its own focus, goal and context; through continuous reasoning you produce new events that move the whole system toward the user's overall goal.

Talking to one goes through message: to hand over work, report a result, ask something, or answer a note it sent you. Both directions use it — a peer's note arrives as input, and your reply is another message.

The events below are how you usually collaborate with a reason worker — and you can send them to yourself as well, to operate on yourself:
- program.query / program.attach / program.detach — list, attach or detach the programs it has mounted
- worker.readme / worker.readme.update — read or update its self-description
- provider.current / provider.list / provider.switch — read or switch its LLM provider
- context.compress — compact its current context, usually your own

### program
Programs are the source code of a worker's capability:
- Use the program worker to add, find and read prompt-type programs and decide what to do from their content; or to write and run script-type programs that carry out a whole pre-orchestrated sequence of event calls.
- Use a reason worker's program.attach / program.detach events to mount the program entries it should watch for and may need, and program.query to list what is currently mounted.

### readme
The readme is an effective way for workers to collaborate: it lets them understand each other quickly.
Keep worker.readme up to date: Markdown, first person, in two parts — what you are responsible for, and what you have been working on lately.

{{if .Playbooks}}## Playbooks (procedures you may consult)
Only metadata is listed here; read the content with program.read by path.
{{range .Playbooks}}- {{.Name}} (path: {{.Path}}): {{.Description}} (tags: {{.Tags}})
{{end}}{{end}}{{if .Instructions}}## Instructions (rules you must follow)
Entries marked [locked] cannot be modified.
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
