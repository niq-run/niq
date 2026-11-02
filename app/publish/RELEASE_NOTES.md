# niq v0.14.1

A polish release on top of v0.14.0: the CLI settles into one visual language,
`-p` means "and the running projects" on every command that touches them, and
the WebUI gains a resizable worker selector plus a few fixes that had been
nagging.

## Highlights

- **`niq restart -p` and `niq stop -p`.** One flag, on both commands, for "the
  running project instances as well as the control plane" — which is what a
  binary upgrade needs, since every process serves the page it was built with.
  `-p` replaces `stop --all`.
- **One shape for the CLI.** `niq status`, `--help` and `niq project` now open
  with the same identity line (the mark and `version: 0.14.1`), start at column
  0, and write projects the same way everywhere: `[pin]`, with the theme colour
  marking what is current.
- **The picker's menus no longer change shape.** Start / Stop / Restart are
  always listed; the ones an instance cannot perform are dimmed and the cursor
  steps over them.
- **The worker selector is resizable** — drag the divider under it, the height
  is remembered, double-click restores the default.

## 1. `-p` on restart and stop

```
$ niq restart -p
niq control stopped pid 66077
   - niq -  version: 0.14.1

[control] running  pid 66294 · since 2026-11-01 23:52
webui    http://localhost:9527
log      ~/.niq/niq.log
stop     niq stop
restart  niq restart

[projects]
demo1 restarted http://localhost:53916
demo2 restarted http://localhost:54069
```

The set is captured before the control plane goes down: whatever answered on its
WebUI port, whether the plane started it, the WebUI did, or a terminal did. Each
instance is stopped through that port (SIGINT — it snapshots on the way out),
the port is waited on, and a new one is started on the same address. A project
that fails is reported in red and the rest continue.

A plain `niq restart` still leaves the instances alone, and a plain `niq stop`
still names the ones it left behind — pointing at `niq stop -p`, its new
spelling.

## 2. One shape for the CLI

```
 - niq -  version: 0.14.1

[control] running  pid 68892 · since 2026-11-01 23:57
webui    http://localhost:9527
log      ~/.niq/niq.log
stop     niq stop
restart  niq restart

[projects]  niq project to start, stop or restart one
[niqA]  stopped  webui 58473  bus 58471  http://localhost:9527/p/niqA/
[pin]   running  webui 57609  bus 57608  http://localhost:9527/p/pin/
```

The two-column left margin is gone, so the mark, the headings, the labels, the
project rows and the usage tables all start at the same edge. The version reads
`version: 0.14.1` — a label and a value rather than one run-on token. Project
names are bracketed everywhere, and `[projects]` says where the list can be
acted on.

## 3. The project picker

```
 - niq -  version: 0.14.1

↑/↓, ⌃n/⌃p or j/k move · enter select · q quit
● [pin]   running  webui 57609  bus 57608  ~/.niq/projects/pin
○ [sep5]  stopped  webui 52218  bus 52217  ~/.niq/projects/sep5
```

- The header is the identity line every other screen prints, a blank line, then
  the keys — `j`/`k` and `⌃n`/`⌃p` work and are now advertised.
- Rows start at column 0. The `>` cursor is gone: the leading dot is the cursor
  — filled in the theme colour on the selected row, a dim ring on the others —
  and the selected name is bracketed in the same colour.
- A plain listing (`niq project | cat`, and `niq status`) has no cursor, so it
  draws no dot; the state is the word after the name.

Choosing a project opens the action menu in the same language:

```
[pat] running

● [Start]
○ [Stop]
○ [Restart]
```

All three are always listed, in that order. On a stopped project Stop and
Restart are dimmed, the cursor skips them, and the menu says "press enter to
start the project". Start on a running project reports "already running" rather
than doing something surprising.

## 4. WebUI

- **The worker selector is resizable.** The divider under it is the drag handle,
  drawn like the sidebar's width grip (a 5px pill riding the line, dimmed until
  hovered). The height is a cap — the list still shrinks to its content and
  scrolls inside it — persists in `localStorage`, and a double-click restores
  the ten-row default.
- **The sidebar footer no longer jumps** when the logo replaces the project
  name: both states sit in a fixed-height row.
- **"status unavailable — restart/stop still work" is gone.** It appeared for
  the few hundred milliseconds before the first control-plane poll answered and
  read as a fault; the menu already offers what it can do.
- **The event detail shows the date** — `2026-11-01 17:39:53 · 1762011593`
  instead of a bare time of day. List rows keep `HH:MM:SS`.

## Upgrade

Rebuild or install the new binary, then:

```
niq restart -p
```

One command now brings the control plane *and* every running project onto it.
Without the flag the projects keep serving the page they were built with, which
is correct but leaves the old UI up.

## Verification

`go build ./...`, `go vet ./app/...` and `go test ./app/...` pass, with new tests
covering the menu's redraw height, the cursor skipping disabled actions, and the
project-state record. Exercised on macOS: `restart -p` and `stop -p` with and
without running instances, the picker's three menus, the selector's drag /
double-click / reload, and the WebUI's URL and banner behaviour.
