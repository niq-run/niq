package host

// registerReadme declares the generic worker.readme capability: peers, the
// WebUI and the worker itself can ask what this worker is for and which tools
// it serves.
func (w *HostWorker) registerReadme() {
	w.RegisterReadme(
		"Read this worker's usage guide: lifecycle tools (spawn/suspend/resume), "+
			"worker.help, and what host deliberately does not own.",
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
thin: it owns spawn/suspend/resume, not the fleet roster (ask the directory
worker who exists) and not the project's declarations.

## Tools

- spawn { type, id?, ... } — create a worker and start it immediately. ` + "`type`" + `
  selects the builder: reason, coding, workspace, program, timer, history,
  directory, mcp, niw, or hiw. Type-specific arguments are passed as top-level
  properties next to ` + "`type`" + `, e.g. a reason worker's ` + "`goal`" + `, ` + "`brief`" + ` and
  ` + "`programs`" + `. Returns ` + "`{worker_id, status: \"created\", type}`" + `.
- suspend { worker_id } — snapshot the worker, stop its goroutine and release
  its bus connection. State stays on disk for a later resume.
- resume { worker_id } — reconnect a suspended worker and start it from its last
  snapshot.
- worker.help { event_name, params? } — per-event help for THIS worker, like a
  CLI's ` + "`-h`" + `. Call it before a non-trivial spawn, e.g.
  ` + "`{event_name: \"spawn\", params: {worker: \"reason\"}}`" + `.

## How to use

1. Prefer an existing worker over spawning a duplicate: ask the directory worker
   with search / describe first.
2. Spawn when a new standing focus is needed, not for a one-off question — for
   that, use message/request to a worker that already exists.
3. suspend / resume take the worker id returned by spawn (or from the roster).

## Notes

- Host manages lifecycles only: it does not list, search or delete workers.
- Spawn is a normal tool, not a privileged API; a reason worker may spawn peers.
- worker.readme (this text) and worker.help (one event's guide) are generic bus
  conventions: every worker may serve either.`
