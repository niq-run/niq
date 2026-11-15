package host

// registerReadme declares the generic worker.readme capability: peers, the
// WebUI and the worker itself can ask what this worker is for and which tools
// it serves.
func (w *HostWorker) registerReadme() {
	w.RegisterReadme(
		"Read this worker's usage guide: lifecycle tools (spawn/suspend/resume/restart/destroy), "+
			"declaration edits (update/archive), the placements a worker can run in, worker.help, "+
			"and what host deliberately does not own.",
		w.readmeText, w.params)
}

// readmeText is the guide returned by worker.readme. Host is static by design
// (it holds no roster), so the guide is not parameterized with live state.
func (w *HostWorker) readmeText() string {
	return hostReadme
}

// hostReadme is the agent-facing usage guide returned by worker.readme.
const hostReadme = `# host — worker lifecycle

This worker creates and manages other workers' lifecycles. It is deliberately
thin: it owns lifecycle, not the fleet roster (ask the directory worker who is
online and what tools each worker serves) and not the project's declarations
(those are the project's own file).

## Placements

Every worker runs in one of three placements. The first two are yours to manage;
the third belongs to whoever runs it.

- ` + "`in-process`" + ` (default) — the host builds the worker and it runs inside the
  host process. ` + "`type`" + ` selects the builder. Full lifecycle including snapshot.
- ` + "`managed-process`" + ` — the host launches ` + "`command`" + ` as its own OS process and
  supervises it (crash restart with backoff, start/stop/restart). It connects back
  to the bus with the credential the host injects. Use this for third-party
  workers that ship as a command: an MCP stdio server, a custom agent.
- ` + "`unmanaged-process`" + ` — the third party starts itself and dials the bus. The
  host only declares it and issues a bus credential; nothing is launched here,
  and there is no local start/stop. Its credential is NOT returned by spawn:
  read the declaration (or the WebUI) to hand the connect configuration out.

## Tools

- spawn { type, id?, placement?, ... } — create a worker and start it. Returns
  ` + "`{worker_id, status: \"created\", type, placement}`" + `. The ` + "`worker_id`" + ` is the id
  actually used — a builder may derive one (a workspace from its path), so never
  assume the id you passed is the id you got.
  Type-specific arguments are top-level properties next to ` + "`type`" + `, e.g. a
  reason worker's ` + "`goal`" + `, ` + "`brief`" + ` and ` + "`programs`" + `.
  For ` + "`managed-process`" + `: ` + "`command`" + ` (argv array, required) and ` + "`cwd`" + ` (must
  exist). Every other argument becomes the child's environment variable; those
  keys are rejected for the other placements.
- suspend { worker_id } — stop it. An in-process worker is snapshotted first; a
  managed-process worker's process is stopped (it persists its own state).
- resume { worker_id } — start a worker that is declared but not running.
  This is also the "start" action: an in-process worker builds from its
  declaration, a managed-process worker is relaunched, an unmanaged one just has
  its identity re-ensured.
- restart { worker_id } — rebuild from the declaration. Construction params
  follow the declaration; what the worker itself persisted survives.
- destroy { worker_id } — stop it and remove its declaration, persisted state,
  bus connection and identity. Irreversible; the removal happens immediately.
- update { worker_id, label?, tags?, description?, params? } — edit the
  DECLARATION only; nothing is started or stopped. Display metadata is visible
  immediately; ` + "`params`" + ` are build input, so they take effect on the next start
  (restart to apply now). Omitted fields stay as they are, empty ones clear.
- archive { worker_id, archived } — move a worker into or out of the archived
  set. Archiving stops it and drops its bus identity but keeps its declaration,
  so it can be restored unchanged. Not a delete.
- worker.help { event_name, params? } — per-event help for THIS worker, like a
  CLI's ` + "`-h`" + `. Call it before a non-trivial spawn, e.g.
  ` + "`{event_name: \"spawn\", params: {worker: \"reason\"}}`" + `.

## How to use

1. Prefer an existing worker over spawning a duplicate: ask the directory worker
   with search / describe first.
2. Spawn when a new standing focus is needed, not for a one-off question — for
   that, use message/request to a worker that already exists.
3. The lifecycle tools take the worker id returned by spawn (or from the
   directory roster).
4. Spawning a third-party process is a real capability decision: ` + "`command`" + ` is
   executed as-is (no shell), and the remaining arguments become the child's
   environment. Prefer ` + "`env:VAR`" + ` references over literal secrets — the
   declaration and the event log are both readable.

## Notes

- Host manages lifecycles and declarations only: it does not list capabilities or
  search. That is the directory worker's job; the two are complements, not
  duplicates.
- Bus ACL is not a host tool: publish/subscribe grants belong to the bus's
  identity control plane, not to a worker's lifecycle. An ACL edit is made
  through the bus identity layer, and it lands in the declaration it derives
  from.
- Spawn is a normal tool, not a privileged API; a reason worker may spawn peers.
- Spawn parameters are checked per type: a key the type does not read fails the
  call and the error lists what it accepts. ` + "`prompt`" + ` is not a reason
  argument — a reason worker's standing instructions are ` + "`instruction`" + `, its
  mission ` + "`goal`" + `. Ask worker.help for the type before a non-trivial spawn.
- worker.readme (this text) and worker.help (one event's guide) are generic bus
  conventions: every worker may serve either.`
