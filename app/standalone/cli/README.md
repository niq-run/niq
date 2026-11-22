# cli

The command-line **entry** and the **daemon lifecycle** for niq.

`publish/cmd` owns the binary's `main`, injects the build identity, declares
which UIs this distribution ships, and calls `cli.Main`. Everything after that
— dispatching a verb, deciding whether a gateway is already running, starting or
stopping it, and reporting on it — lives here.

This package is not a UI client. Terminals that *talk to* a gateway live in
`app/client` (`tui` is the interactive one); they only need a `contract.Client`
and a base URL. `cli` is the layer that makes sure such a gateway exists, which
is why it is also the only place that holds process/port policy.

## Where it sits

```
publish/cmd ──► cli ──► gateway ──► launcher ──► project
                 │
                 ├──► client/tui   (only for the `niq i` picker)
                 └──► helper, store, contract
```

Two roles share this package, and they are worth keeping distinct when adding
code:

| Role | Who | May it prompt the user? |
|---|---|---|
| **Front-end** (short-lived) | `runDefault`, `runShell`, `runInteractive`, `runRestart`, `runStop` | Yes — it owns the terminal. |
| **Daemon start path** (long-lived) | `runShellForeground` → `gateway.Run` | No — `startBackground` spawns it with the null device as stdio. |

The port-conflict choice (below) belongs to the front-end for exactly that
reason: the daemon process has no stdin to ask on.

## Port conflicts

The gateway only **detects** a taken address (`gateway.PortInUseError`). What to
do about it is a lifecycle decision that needs a terminal, so it lives here in
`ensure.go` and runs *before* the daemon is spawned.

Every start path (bare `niq`, `niq w`, `niq i`, `niq restart`) goes through
`ensureGateway`/`resolvePortConflict`:

1. **Our own gateway first.** If `shell.json` names a live gateway, the request
   is reported (same address) or rejected (different address) — never offered as
   a process to kill. `niq w -f` keeps the same check before resolving.
2. **Pre-flight.** `helper.ProbeListen` binds and immediately closes the exact
   listen address; a dynamic port (`:0`) skips this, since it cannot conflict.
   A failure that is not `EADDRINUSE` (permission, bad address) is returned as-is:
   stopping a process would not fix it.
3. **Decide.** With a terminal (`term.IsTerminal(stdin)`) the operator chooses:
   - `k` — stop the holder. `helper.Kill` sends `SIGTERM`, waits up to 3s, then
     `SIGKILL`s whatever still listens; the address is re-probed before starting.
     On platforms without discovery this becomes "choose another port".
   - `n` — start elsewhere. The suggestion is the next bindable port on the
     same host (`nextFreeAddr`), so exposure is unchanged; an explicit port is
     accepted and re-probed until it is free.
   - `c` — cancel, exiting quietly.
   Without a terminal the conflict becomes an actionable error that names the
   holder (`pid 123 (nginx)`) and the two ways out; the detached child
   (`w --foreground`, null stdio) never blocks on a prompt.
4. **Spawn.** The resolved address is handed to `startBackground`, so the child
   (`w --foreground --addr <resolved>`) binds what was chosen and records it in
   `shell.json` through `OnReady`.

Port-holder discovery and signalling, address probing and bind shaping all
come from `app/standalone/helper` — the stdlib-only package underneath the
single-machine form. Discovery is best-effort (lsof) and reports
`ErrUnsupported` elsewhere, which still leaves "start on another port"
available. The decision itself is covered by a table test in `ensure_test.go`
through injected `portOps` (no real socket or process).

## Files

| File | Responsibility |
|---|---|
| `main.go` | Entry point (`Main`), verb dispatch (`w`, `i`, `status`, `stop`, `restart`, `update`, `completion`), `--help` text, and the start path shared by bare `niq` and `niq w`. |
| `daemon.go` | Background lifecycle: the `shell.json` record, `startBackground`/`runShellForeground`, liveness probing, stop/restart, and the status report (gateway + project table). |
| `ensure.go` | Port-conflict resolution before starting the daemon: pre-flight probe, the injected asker, and the non-interactive actionable error. |
| `status_json.go` | `niq status --json`: the same snapshot as the human report, for clients that render their own UI (the macOS app). |
| `ui.go` | Terminal presentation only: colour detection, the banner, rows, labels, path shortening. No lifecycle logic. |
| `complete.go` | `niq completion <shell>`: generates bash/zsh/fish completion scripts. |
| `completion_install.go` | `niq completion install`: writes that script into a directory the shell already scans (idempotent). |
| `update.go` | The npm-backed updater: `niq update`, plus the timid once-a-day passive check behind a bare `niq`. |
| `detach_unix.go` | Unix platform primitives: `Setsid` detach, `SIGTERM` to stop, signal-0 liveness. |
| `detach_windows.go` | Windows equivalents: detached process creation flags, hard kill, `FindProcess` liveness. |
| `ensure_test.go` | The conflict decision table, driven through fake `portOps` and askers (no sockets, no signals). |
| `daemon_test.go` | The `shell.json` round-trip. |
| `update_test.go` | Version comparison and update-cache behaviour. |

## Invariants

- The daemon is always started detached (`startBackground`), so closing the
  terminal does not stop it; `niq stop`/`niq restart` operate on the recorded
  pid, never on a guessed one.
- A stale `shell.json` never reads as "running": liveness needs both a live pid
  and an answering address.
- Nothing in this package imports `app/client` except `main.go`/`daemon.go`
  reaching for the `niq i` picker; the reverse never happens.
