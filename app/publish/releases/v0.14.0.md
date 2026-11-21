# niq v0.14.0

A command-line release. `niq` is now a service you start once and ask after,
`niq project` is a list you act on instead of a command you retype, and the
control plane stopped treating the projects it had launched as different from
the ones it had not. The browser side follows the same idea: an address keeps
working whether or not the project behind it is running, and the tab you are
looking at is the address you are at.

## Highlights

- **`niq` runs in the background.** Start it, read the address, get your prompt
  back; closing the terminal no longer stops the control plane. `niq status`,
  `niq stop [--all]` and `niq restart` manage it, and `niq -f` still runs it
  here.
- **`niq project` is a picker.** State, ports and directory per project; enter
  starts, stops or restarts the one you chose, and the list stays up for the
  next one.
- **An instance is the port it holds.** A project started by hand, by the WebUI,
  or by a control plane that has since restarted are all stopped and restarted
  the same way. Restart used to do nothing at all for the ones the plane had not
  spawned itself.
- **A stopped project's address is useful.** `/p/<id>/` lands on the projects
  list with a start prompt instead of a 502, and the tab you are on is in the
  URL, so Back and reload behave.
- **Completion for bash, zsh and fish**, with the project ids completed live.

## 1. `niq` is a service

A bare `niq` re-executes itself detached — its own session on unix, a detached
console process on windows — waits until the listen socket is bound, prints the
WebUI address, the log path and the two commands that matter, and returns to
the prompt. Repeating it is how you ask after the service: when it is already
running, `niq` prints exactly what `niq status` prints.

The running process is recorded in `<niq root>/control.json` (`pid`, requested
and bound address, start time), written through a temp file and a rename the
moment the socket is bound. `status`, `stop` and `restart` read that record, and
a record whose process is gone is cleared instead of being reported as running.

- `niq stop` stops the control plane. Projects are their own processes, so they
  keep serving; the command says which ones it left behind, and `niq stop --all`
  interrupts them too (SIGINT — they snapshot on the way out).
- `niq restart` brings the plane back on the address it was started with. The
  basic-auth pair still comes from `~/.niq/common/auth.json`, so `--auth` is not
  needed a second time.
- `niq -f` (`niq control --foreground`) is the old attached behaviour: for
  systemd, a container, or watching it work.

`niq control --addr …` also stopped ignoring its flags — `flag.Parse` used to
stop at the subcommand word — and an unknown word is now an error instead of a
silent start.

## 2. `niq project`

`niq project` lists every project on one screen — state, webui/bus ports and
directory — and moves with the arrow keys, `j`/`k` or `⌃n`/`⌃p`. Enter offers
the actions that fit that instance: start when it is stopped, stop or restart
when it runs. `niq project list` is the same command.

The list stays up after an action. States reload, the cursor stays on the
project just acted on, and the result goes on a status line under the list,
replaced each time rather than appended, so the screen never scrolls away from
what you are looking at. `q`, `Esc` and `⌃C` leave; a failed action prints in
red and returns you to the list.

Stop and start work through the instance's own state — the port for stopping,
the persisted ports for starting — so they behave the same whether or not a
control plane is running. Without a terminal `niq project` prints the same
table; `--ids` prints just the ids, which is what completion calls.

## 3. The control plane stops owning processes

The control plane kept a map of the `niq project run` processes it had spawned
and used it as the authority for "is it running", "stop it" and "restart it".
Anything else — a project started in a terminal, or one that outlived a restart
of the plane — took a different path, and the two disagreed: restart did nothing
at all for the second kind.

An instance is now identified by the ports in its `project.json` and by nothing
else. Running means the persisted WebUI port answers; stop interrupts whoever
listens on it; restart does that, waits for the address to free, and relaunches
on it. The plane still reaps the children it spawns, but only to log a crash —
the map, the mutex and the second code path per action are gone.

## 4. The WebUI: addresses that keep working

- Opening a stopped project's `/p/<id>/` redirects to the projects list with a
  start prompt. Cancelling leaves you on the list, which is the point: a dead
  link should land somewhere you can act. Starting from the prompt continues
  into the project.
- Management tabs (`projects`, `templates`, `providers`) and the project's own
  views (`talk`, `events`, `approvals`, `workers`, `mcp`, `programs`) live in
  the URL (`?panel=…`, `?view=…`). Back and Forward walk what you visited, a
  reload lands on the same tab, and those pages do not subscribe to the
  project's event stream — so neither "stream dropped" nor "project not
  running" appears on a page that is not showing the project.
- The control page no longer reports a dropped stream at all: context starts
  optimistically as `project`, so a stream used to be opened once against the
  control plane's 404 before `/api/context` answered.
- Static assets are served relative to the page that embedded them. A project
  page loads its own bundle through `/p/<id>/assets/…`; the absolute `/assets/…`
  it used to request meant an older page asked the new control plane for a file
  it no longer shipped, and the browser refused it as `text/plain`.

## 5. One look, and completion

The report has one look now: theme colour `#3C78B4` for the mark, the version
and URLs; a one-line chip with the wordmark between rules; `version-…` rather
than git describe's `v…`; bracketed headings over a single left margin. Colour
is emitted only when stdout is a terminal — `NO_COLOR` and pipes get plain text.

`niq completion bash|zsh|fish` prints a script covering subcommands, flags and
the project ids, read live from `niq project list --ids`.

## 6. Smaller changes

- The `event_recipients` backfill is gone from `migrate()`. The table is written
  by `Append`; scanning every legacy row on each open is not worth keeping, and
  no compatibility path is left for schemas already migrated.
- Permissions moved into a dialog, and the forms share one set of styles.
- The talk scroller settles its follow rules, and workers can be addressed by
  display name in the talk input.

## Upgrade notes

- **`niq` backgrounds by default.** Scripts, containers and CI that expect the
  process in the foreground need `niq -f`.
- **`niq restart` after upgrading.** A control plane started by an older binary
  has no record in `control.json`: `niq status` reports it as not running while
  it still holds the port, and a new `niq` fails with "address already in use".
  Stopping the old process once is enough.
- **Restart running projects to get the new WebUI.** Each process serves the
  page and assets it was built with, so an older project keeps rendering the
  older UI — it no longer breaks, which it did while asset URLs were absolute.
- **`niq stop` leaves projects running.** That was already true; it is now said
  out loud, and `niq stop --all` is the whole-machine stop.
