# launcher — project instances, identified by their port

`launcher` owns project *instances*: `List`, `Start`, `Stop`, `Restart`,
`IsRunning` and `WaitStopped`, plus the private child-process protocol that turns
the binary into a project runtime. It is the only package that knows how a
project process is invoked and how a running instance is recognised.

It is not a daemon. It renders nothing, listens on no port, and imports neither
`cli` nor `app/client`; its dependencies are `contract`, `helper`, `project` and
`store`. "An instance" is never a table of parent/child links — it is an address
that either answers or does not.

## Where it sits

```
cli ──► gateway ──► launcher ──► project     (Start: exec self, ChildVerb, id)
                       │
                       └──► contract, helper, store

gateway process    … → launcher.Start(id)
                            │ exec(self, ChildVerb, id)
                            ▼
project process    main → cli.Main → launcher.IsChild → ChildMain → project.RunProject
```

## An instance is a port

A project's identity here is its persisted WebUI port. `IsRunning(p)` is
`IsRunningPort(p.Ports.WebUI)`, and `IsRunningPort` is just a dial through
`helper.IsServingPort` (port 0 reads as "not running"). There is no registry of
who started what, which is the point: a project started by `Start`, one started
by hand with the same command, and one that outlived the shell that ran it are
the same instance, because they own the same port. `List` therefore probes run
state now, per project, rather than reading it from disk.

`ViewOf` is the one conversion from the stored shape to the wire shape
(`contract.View`): it resolves `store.ProjectDir`, probes the port for `Running`,
and carries only `ID`, `CreatedAt`, `Ports`, `Dir` and `Running`.

`Start` only execs. It re-runs `os.Executable()` with the private
`ChildVerb` and the project id, detaches the child in the platform's own way,
forwards its stdout/stderr, and then waits up to 20 seconds for the WebUI port to
answer. The child's exit result is kept, so a child that dies during startup is
reported as a failure naming the exit plus the tail of the project log, instead
of a silent timeout. Nobody runs project code in the caller's process: that is
`ChildMain`'s job, and `ChildMain` only ever runs inside the new one.

`Stop` interrupts whoever listens on the WebUI port and then waits. The wait is
bounded and two-part: the port must stop answering, and — where holder discovery
exists — the discovered processes must actually exit. It waits on the *process*
because shutdown closes listeners before it finishes writing state, so acting on
"the port is free" would race that last write. `Stop` reports `ErrNotRunning`
when nothing was listening; `WaitStopped` is the separate, port-only wait for a
caller that wants to block until the address is quiet.

## Files

| File | Responsibility |
|---|---|
| `launcher.go` | The whole lifecycle and the child protocol: `List`, `ViewOf`, `Start`, `Stop`, `Restart`, `IsRunning`, `WaitStopped`, `LogHint`, `ChildVerb`/`IsChild`/`ChildMain`, and the bounded waits behind them. |
| `detach_unix.go` | Unix detachment: `Setsid`, so the launching shell is not the project's controlling terminal. |
| `detach_windows.go` | Windows detachment: `CREATE_NEW_PROCESS_GROUP | DETACHED_PROCESS`, so closing the console does not take the project down. |
| `launcher_test.go` | Startup-failure reporting: `waitStart` surfaces the child's exit and points at the log, and `logTail` returns only trailing lines. |

## Invariants

- **Identity is the port.** No parent/child bookkeeping exists, so a hand-started
  instance and an orphaned one are found and stopped like any other.
- **`Start` reuses the persisted ports** (it passes no address flags), so a
  restart lands on the same addresses rather than jumping.
- **Starting a running project is not an error.** The same instance comes back
  with `Already` set.
- **`ChildVerb` is not a user command.** The word lives only in this package;
  `cli.Main` asks `launcher.IsChild` before it dispatches any verb and hands off
  from there, so the rest of the CLI never learns the verb exists.
- **`ChildMain` runs in the new process only.** `Start` execs; it never runs
  project code itself — which is why "the gateway runs projects" is false.
- **`Stop` reports on what it found, not on what happened next.** A listener that
  was interrupted yields success even if the bounded wait expired; `WaitStopped`
  is what tells a caller the address actually went quiet.
