package host

import (
	"strings"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// registerHelp declares worker.help on the host: asking for "spawn" returns
// either the general spawn reference or, when params.worker names a worker
// type, a guide specialized for that type.
func (w *HostWorker) registerHelp() {
	w.RegisterHelp(map[event.EventType]baseworker.HelpFunc{
		TypeSpawn: spawnHelp,
	})
}

// spawnHelp answers worker.help for spawn. params.worker selects the guide —
// it is a HELP selector, not a spawn argument: spawn itself takes `type`, not
// `worker`. An unrecognized selector still returns the general reference, with
// a note that no type-specific guide exists yet.
func spawnHelp(params map[string]any) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(baseworker.ArgString(params, "worker"))) {
	case "reason":
		return spawnReasonHelp, true
	case "":
		return spawnGeneralHelp, true
	default:
		return spawnGeneralHelp + noTypeHelp, true
	}
}

const spawnGeneralHelp = `# spawn

Create a worker and start it immediately. The host registers the new worker's
bus identity and builds it in-process; the reply is:

` + "```json" + `
{"worker_id": "<id>", "status": "created", "type": "<type>"}
` + "```" + `

## Arguments

Required:
- ` + "`type`" + ` — worker type to spawn: reason, coding, workspace, program, timer,
  history, directory, mcp, niw, or hiw.

Common:
- ` + "`id`" + ` — worker id, unique on the bus. Some builders derive one (workspace
  derives it from ` + "`path`" + `); reason and coding require it.
- ` + "`tags`" + ` — array or comma-separated string of slash-path labels for the WebUI
  selector, e.g. ` + "`[\"ops/backlog\"]`" + `.
- ` + "`description`" + ` — one-line purpose note for the WebUI.

Type-specific arguments are passed as top-level properties next to ` + "`type`" + `.
For example workspace takes ` + "`path`" + ` (or ` + "`mounts`" + `) and ` + "`approver`" + `.

## Type-specific guides

Ask again with ` + "`params.worker`" + ` set to the type you are about to spawn:

` + "```json" + `
{"event_name": "spawn", "params": {"worker": "reason"}}
` + "```" + `

Currently documented: ` + "`reason`" + `.

## Minimal request

` + "```json" + `
{"type": "reason", "id": "analyst", "goal": "Summarize new issues every morning."}
` + "```" + `
`

const spawnReasonHelp = `# Spawn a reason worker

Call this worker's ` + "`spawn`" + ` event with ` + "`type: \"reason\"`" + `. The host starts the new
worker immediately; the reply is ` + "`{\"worker_id\": \"<id>\", \"status\": \"created\", \"type\": \"reason\"}`" + `.
Address it by that id with ` + "`message`" + `, ` + "`request`" + ` or ` + "`worker.readme`" + `, or manage it with
` + "`suspend`" + ` / ` + "`resume`" + `.

` + "```json" + `
{
  "type": "reason",
  "id": "analyst",
  "goal": "Watch the issue tracker and summarize new bugs every morning.",
  "brief": "You are taking over from me; the tracker URL and labels are in the goal."
}
` + "```" + `

## Required

- ` + "`type`" + ` — the literal ` + "`\"reason\"`" + `.
- ` + "`id`" + ` — worker id, unique on the bus. The reason builder does not derive one,
  so omitting it fails the call.

## Identity and attention

These decide what the worker is and what it pays attention to:

- ` + "`goal`" + ` (string) — the standing mission. Seeded as a dedicated ` + "`goal`" + ` instruction
  program, rendered into the system prompt every round, so it survives context
  compression. Use it for what the worker is here to do, not a one-off task.
- ` + "`instruction`" + ` (string) — standing instructions, seeded as an ` + "`instruction`" + ` program.
  Used only when ` + "`programs`" + ` is empty. Stays in the system prompt; not compacted away.
- ` + "`brief`" + ` (string) — handover context, seeded as the first user message. It is
  working material and can be compacted; put durable facts in ` + "`goal`" + ` or a program.
- ` + "`programs`" + ` (array) — extra programs, each:
  ` + "`{\"name\", \"content_type\": \"instruction\"|\"playbook\", \"description\"?, \"content\"?}`" + `.
  Instructions stay in the system prompt; playbooks are behavior the worker can
  load on demand. When non-empty, ` + "`instruction`" + ` is ignored.
- ` + "`readme`" + ` (string) — seed for ` + "`worker.readme`" + `, the self-description peers and the
  WebUI read. Markdown is rendered by the WebUI; keep it short. The worker can
  revise it itself later.
- ` + "`events`" + ` (array of event types) — events this worker should convert into input,
  e.g. ` + "`[\"github.issue.new\"]`" + `.
- ` + "`subscriptions`" + ` (array) — broadcast subscriptions; entries are an event type
  string or ` + "`{\"type\", \"source\"}`" + `. This REPLACES the reason default of
  ` + "`worker.ready`" + `, ` + "`worker.gone`" + `, ` + "`worker.discover`" + `, ` + "`worker.abort`" + `, ` + "`worker.input`" + `.
  Directed calls and their replies still reach the worker regardless.
- ` + "`publish`" + ` (array) — publish grants; defaults to ` + "`[\"*\"]`" + ` so the worker can forward
  calls under peers' own event types. Rarely worth overriding.

## Model

- ` + "`provider`" + ` (string) — provider name from the project's provider config. Omit to
  use the project default provider.
- ` + "`model`" + ` (string) — model name. Omit to use the provider's default model.
- ` + "`base_url`" + ` / ` + "`api_key`" + ` (string) — per-worker overrides. Prefer relying on the
  project provider config; a per-worker key lands in the worker declaration.

## Context budget

- ` + "`context_window`" + ` (int) — model window in tokens; 0 disables budget handling.
  Normally supplied by the provider/model config.
- ` + "`budget_soft`" + ` (ratio, default 0.85) and ` + "`budget_hard`" + ` (ratio, default 0.97) —
  window occupancy that triggers a compression reminder / forced compression.
- ` + "`tail_tokens`" + ` (int, default 10000; legacy alias ` + "`keep_tail`" + `) — token budget of
  recent transcript kept verbatim after compression. A tool call and its
  results are never split.
- ` + "`compact_directive`" + ` (string) — overrides the built-in summarizer prompt used
  when compressing.
- ` + "`rotate_rounds`" + ` (int, default 2) — rounds retained by ` + "`context.rotate`" + `; that
  tool is currently disabled.

## Display metadata

Stripped before the builder sees them; they land in the worker declaration:

- ` + "`tags`" + ` — array or comma-separated slash-path labels, e.g. ` + "`\"ops/backlog\"`" + `.
- ` + "`description`" + ` — one-line purpose note.

## Note on the selector

` + "`params.worker: \"reason\"`" + ` is only how you asked for this guide. The actual
spawn argument is ` + "`type: \"reason\"`" + `, not ` + "`worker`" + `.
`

// noTypeHelp appends an honest note when params.worker names a type with no
// specialized guide yet.
const noTypeHelp = `
> No type-specific guide exists for that ` + "`params.worker`" + ` value yet; this is the
> general spawn reference. The type-specific arguments still apply as top-level
> properties next to ` + "`type`" + `.
`
