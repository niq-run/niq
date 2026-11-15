package host

import (
	"strings"

	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// registerHelp declares worker.help on the host: asking for "spawn" returns
// either the general spawn reference or, when params.worker names a worker
// type, a guide specialized for that type. The lifecycle verbs share one
// reference, since the placement rules that matter are common to them.
func (w *HostWorker) registerHelp() {
	w.RegisterHelp(map[event.EventType]baseworker.HelpFunc{
		TypeSpawn:   spawnHelp,
		TypeSuspend: staticHelp(lifecycleHelp),
		TypeResume:  staticHelp(lifecycleHelp),
		TypeRestart: staticHelp(lifecycleHelp),
		TypeDestroy: staticHelp(lifecycleHelp),
		TypeUpdate:  staticHelp(updateHelp),
		TypeArchive: staticHelp(archiveHelp),
	})
}

// staticHelp adapts a fixed guide to a HelpFunc.
func staticHelp(text string) baseworker.HelpFunc {
	return func(map[string]any) (string, bool) { return text, true }
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

// lifecycleHelp is the shared guide for suspend / resume / restart / destroy:
// all four take one worker id and differ only in what they do to it.
const lifecycleHelp = `# suspend / resume / restart / destroy

All four lifecycle tools take one argument:

` + "```json" + `
{"worker_id": "<id from spawn or the directory roster>"}
` + "```" + `

- ` + "`suspend`" + ` — stop it. An in-process worker is snapshotted, stopped and
  disconnected; a managed-process worker's OS process is stopped (it persists
  its own state). The worker stays declared and can be resumed.
- ` + "`resume`" + ` — start a declared-but-stopped worker. This is the start action:
  an in-process worker builds from its declaration (or resumes from its
  snapshot), a managed-process worker is relaunched, an unmanaged-process worker
  only gets its identity re-ensured — it starts itself.
- ` + "`restart`" + ` — rebuild from the declaration. Construction params follow the
  declaration edit; what the worker itself persisted survives.
- ` + "`destroy`" + ` — stop it and remove its declaration, persisted state, bus
  connection and identity. Irreversible; the removal happens immediately.

## Notes

- ` + "`unmanaged-process`" + ` workers have no local lifecycle: suspend/restart are
  refused, because the host does not run them.
- A protected worker (the host itself, the WebUI's HIW) cannot be suspended,
  restarted or destroyed.
`

// updateHelp is the guide for the declaration-editing tool.
const updateHelp = `# update

Edit a worker's declaration without touching its runtime state.

` + "```json" + `
{"worker_id": "<id>", "label": "值班", "tags": ["ops"], "params": {"model": "m"}}
` + "```" + `

- Omitted fields stay as they are; an empty string, list or object clears that
  field. Every field is optional except ` + "`worker_id`" + `.
- ` + "`label`" + ` / ` + "`tags`" + ` / ` + "`description`" + ` are display metadata: visible immediately.
- ` + "`params`" + ` replaces the construction params WHOLESALE. Params are build input,
  so the change applies on the next start — use ` + "`restart`" + ` to apply it now.
- Bus ACL is not editable here. Publish/subscribe grants are bus identity data,
  owned by the bus control plane, not by a worker's lifecycle backend.
`

// archiveHelp is the guide for the archive tool.
const archiveHelp = `# archive

Move a worker in or out of the archived set.

` + "```json" + `
{"worker_id": "<id>", "archived": true}
` + "```" + `

- Archiving stops a live worker (an in-process one is snapshotted first), moves
  its declaration to workers.archived.json and drops its bus identity. The
  declaration is kept verbatim, so restoring it brings the worker back exactly
  as it was.
- A ` + "`unmanaged-process`" + ` worker has nothing local to stop; archiving only hides it.
- Unarchiving moves the declaration back and starts the worker again (in-process
  builds, managed-process launches, unmanaged re-provisions its identity).
- archive is not destroy: the declaration and its state survive.
`

const spawnGeneralHelp = `# spawn

Create a worker and start it. The reply is:

` + "```json" + `
{"worker_id": "<id>", "status": "created", "type": "<type>", "placement": "<placement>"}
` + "```" + `

` + "`worker_id`" + ` is the id actually used: some builders derive one (a workspace from
its ` + "`path`" + `), so it can differ from the ` + "`id`" + ` you passed — or be the id you must
use when you passed none.

## Arguments

Required:
- ` + "`type`" + ` — worker type: reason, coding, workspace, program, timer, history,
  directory, mcp, niw, or hiw. For ` + "`in-process`" + ` it selects the builder; for a
  process placement it labels the worker's identity.

Placement:
- ` + "`placement`" + ` — ` + "`in-process`" + ` (default), ` + "`managed-process`" + ` or
  ` + "`unmanaged-process`" + `.
- ` + "`command`" + ` — argv array. Required for ` + "`managed-process`" + `: the host launches it
  as an OS process and supervises it. Rejected elsewhere. No shell: the array is
  executed as-is.
- every other argument — for ` + "`managed-process`" + `, all remaining keys become the
  child's environment variables (flat string values). Prefer ` + "`env:VAR`" + `
  references for secrets.
- ` + "`cwd`" + ` — child working directory (` + "`managed-process`" + ` only; must exist).

Common:
- ` + "`id`" + ` — worker id, unique on the bus. Required for a process placement.
- ` + "`tags`" + ` — array or comma-separated string of slash-path labels for the WebUI
  selector, e.g. ` + "`[\"ops/backlog\"]`" + `.
- ` + "`description`" + ` — one-line purpose note for the WebUI.
- ` + "`label`" + ` — display name (备注名) the WebUI shows in place of the id.

Type-specific arguments are passed as top-level properties next to ` + "`type`" + `.
For example workspace takes ` + "`path`" + ` (or ` + "`mounts`" + `) and ` + "`approver`" + `.

## Process example

` + "```json" + `
{
  "type": "mcp",
  "id": "mcp-filesystem",
  "placement": "managed-process",
  "command": ["npx", "-y", "@modelcontextprotocol/server-filesystem", "/data"],
  "GITHUB_TOKEN": "env:GITHUB_TOKEN"
}
` + "```" + `

A process worker connects back to the bus on its own: the host injects
` + "`NIQ_BUS_URL`" + `, ` + "`NIQ_WORKER_ID`" + `, ` + "`NIQ_WORKER_CREDENTIAL`" + ` and a persistent
` + "`NIQ_STATE_DIR`" + `. An ` + "`unmanaged-process`" + ` worker gets a durable credential instead
and is never launched here — do not ask for its credential through spawn; it is
the declaration's field.

## Parameters are checked

Every type accepts only the parameters it reads. A parameter the type does not
read fails the call instead of being ignored, and the error lists what that type
does accept. That is deliberate: a silently dropped ` + "`prompt`" + ` (reason reads
` + "`instruction`" + ` and ` + "`goal`" + `) would look like a successful spawn that never
initialised anything.

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

The endpoint and its credentials live on the provider entry in provider.json:
a worker never carries its own ` + "`base_url`" + ` / ` + "`api_key`" + `. To use a different
endpoint, add it as a provider entry and reference it by name.

## Context budget

- ` + "`budget_soft`" + ` (ratio, default 0.85) and ` + "`budget_hard`" + ` (ratio, default 0.97) —
  window occupancy that triggers a compression reminder / forced compression.
  The window itself is never configured here: it is the selected model's
  window, taken from provider.json's per-model ` + "`context_window`" + ` (or the
  provider API). If neither declares one, budget handling stays off rather
  than guessing a size.
- ` + "`tail_tokens`" + ` (int, default 10000; legacy alias ` + "`keep_tail`" + `) — token budget of
  recent transcript kept verbatim after compression. A tool call and its
  results are never split.
- ` + "`compact_directive`" + ` (string) — overrides the built-in summarizer prompt used
  when compressing.

## Display metadata

Stripped before the builder sees them; they land in the worker declaration:

- ` + "`label`" + ` — display name (备注名) shown in place of the id.
- ` + "`tags`" + ` — array or comma-separated slash-path labels, e.g. ` + "`\"ops/backlog\"`" + `.
- ` + "`description`" + ` — one-line purpose note.

## Collaboration scope

- ` + "`collaborators`" + ` (array of reason worker ids) — the reason peers this worker
  may collaborate with. Omitted means every reason worker is visible; when given
  it is an allowlist (empty means none). Out-of-scope reason peers are invisible
  to it and cannot be messaged or called; capability workers are unaffected.

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
