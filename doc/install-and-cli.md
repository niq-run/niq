# Install, upgrade and the CLI

> Status: as-built (2026-11-21). English · [简体中文](install-and-cli.zh-CN.md).
>
> How to get `niq` onto a machine, how to move it forward, and what every
> command and flag does. The directory layout is in
> [directory-layout.md](directory-layout.md); product orientation is in
> [README](../README.md).

## 1. Install

Three ways in; each one puts the `niq` command on your PATH. **Nothing starts
until you run it** — installing does not launch a daemon.

None of them need a JS runtime: the program worker runs script programs (PTC)
inside an embedded QuickJS engine (`modernc.org/quickjs`, pure Go), so there is
no `node` process to spawn, and scripts see only the SDK the worker injects —
no `process`, `require`, `fetch` or dynamic `import()`.

### npm (recommended)

```sh
npm install -g @niq.run/niq
```

The package ships prebuilt binaries for macOS, Linux and Windows (x64 and
arm64) and adds two conveniences on top of the command:

- **macOS**: `niq.app` is copied to `~/Applications`, so Spotlight and Finder
  see it, and `miq` opens the menu bar app (the same menu as opening the app
  directly). `~/.niq/bin/niq` is also linked to this install's binary, because a
  GUI launch inherits no PATH and cannot run a node shim.
- **Everywhere but Windows**: the shell completion script is written where your
  shell already looks (see [§3.6](#36-shell-completion)), so tab completion
  works in the next shell without editing a dotfile.

Both are best-effort: an install never fails because a convenience could not be
written. `NIQ_NO_APP=1` skips the app, `NIQ_NO_COMPLETION=1` skips completion.

`niq update` is how this install stays current — see [§2](#2-upgrade).

### Install script (no npm)

```sh
curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/app/publish/cmd/install.sh | sh
```

It detects `linux`/`darwin` and `amd64`/`arm64`, downloads that release's
`niq_<os>_<arch>.tar.gz`, installs the binary to `~/.niq/bin`, verifies it runs,
installs completion, and appends the directory to your shell's rc file if it is
not already on `PATH`.

| Variable | Effect |
|---|---|
| `NIQ_INSTALL_DIR` | install somewhere other than `~/.niq/bin` |
| `NIQ_VERSION` | install an exact release tag instead of the latest |
| `NIQ_NO_MODIFY_PATH=1` | do not touch a shell rc file; you add the PATH yourself |
| `NIQ_NO_COMPLETION=1` | skip the completion install |

Needs `curl` (or `wget`) and `tar`. Windows is not supported by this script —
use npm.

### From source

Requires **Go 1.26+** (the embedded JS engine requires it) and **Node 20+** for
the WebUI bundle.

```sh
git clone https://github.com/niq-run/niq
cd niq
make install
```

`make install` rebuilds the WebUI bundle, builds `./bin/niq`, then symlinks it
into `~/.local/bin` (`PREFIX=/some/bin` for another directory), so `niq` works
from any shell once that directory is on your PATH. It never builds silently
from nothing: if `./bin/niq` is missing it tells you and stops.

Run `make build` instead if you would rather keep the binary at `./bin/niq` and
call it from the checkout.

The bundle is committed (`go:embed`-ed), and `make build niq` takes it as it is:
rebuilding the SPA is a separate step (`make build webui`). A plain `make build`
does everything in order — bundle, CLI, app — and naming targets together works
in any order (`make build niq webui` still builds the bundle first).

### Menu bar (macOS)

The menu bar app is part of the macOS install, not a separate channel: the npm
package copies `niq.app` to `~/Applications` and puts `miq` on your PATH (`make
build miq` builds both for a checkout). `miq` opens it; running it again focuses
the menu.

```sh
miq              # puts niq in the menu bar; running it again focuses it
```

Release builds (`niq_mac_darwin_universal.zip`) are ad-hoc signed, not
notarized, so a freshly downloaded `niq.app` needs right-click → Open the first
time — or `xattr -d com.apple.quarantine niq.app`.

### Verify

```sh
niq --version      # prints the version (and the build time, if there is one) and exits
```

## 2. Upgrade

The binary is the unit of upgrade: gateway and project processes each keep
running the binary they started with, so **every path below ends with
`niq restart -p`**.

| Installed via | Upgrade with | Notes |
|---|---|---|
| npm | `niq update` | runs `npm install -g @niq.run/niq@latest`; follows the npm registry |
| install script | re-run the `curl` line | picks the latest GitHub release; `NIQ_VERSION=x.y.z` pins one |
| source | `git pull && make install` | rebuilds the bundle and the CLI, replaces the symlink |

Finish with:

```sh
niq restart -p
```

`-p` restarts the running projects as well. Restarting matters because asset
URLs are relative: a project page loads *its own* bundle, so an instance left
running keeps rendering the old UI even though the new binary is on disk.

### The passive update check

A bare `niq` asks the npm registry at most once a day (the answer is cached in
`~/.niq/update-check.json`) and, when a newer release exists, mentions it at the
end of the output — every time you start, from the cache, until you install it.
The check is deliberately timid: 2-second timeout, skipped when stdout is not a
terminal, and its own failures are never reported. `NIQ_NO_UPDATE_CHECK=1`
turns it off.

To pin or roll back: `NIQ_VERSION=0.14.0` with the install script, or
`npm install -g @niq.run/niq@0.14.0` with npm.

## 3. The CLI

### 3.1 The model in one paragraph

`niq` is two layers. The **gateway** is a long-lived process listening on one
port (default `127.0.0.1:9527`); it is the only HTTP endpoint you ever expose,
serving the WebUI, the project-management API, and every project's page at
`/p/<id>/`. A **project** is an isolated agent instance — its own event bus, its
own worker set, its own WebUI — running as **its own process** on its own
loopback ports. So closing the gateway does not stop projects, and restarting a
project does not touch the gateway. An instance is identified by its persisted
ports, not by who started it: the WebUI, the picker and a project started by
hand all act on the same thing.

There are no project commands: creating, starting, stopping and restarting live
in the WebUI and the picker (`niq i`), both clients of the same HTTP API. The
CLI's job is to ensure the gateway and show you what is up.

### 3.2 First run

```sh
niq
```

This starts the gateway **detached** from the terminal, prints where it is
reachable, and returns to the prompt — closing the shell (or losing an SSH
session) no longer stops it. Running `niq` again reports instead of starting a
second one: it is exactly `niq status`, so one command covers both "start it"
and "is it up?".

```
[gateway] running  pid 4711 · since 2026-11-01 23:57
webui    http://localhost:9527
log      ~/.niq/niq.log
stop     niq stop
restart  niq restart

[projects]  niq i to start, stop or restart one
[pin]    running  webui 57609  bus 57608  http://localhost:9527/p/pin/
[notes]  stopped  webui 57621  bus 57620  http://localhost:9527/p/notes/
```

A project's address works whether or not it is running: opening a stopped
project's `/p/<id>/` lands on a start prompt, not an error page. A refused port
in the table means that instance is not running.

Open **http://localhost:9527** for the WebUI: project list, create-from-template,
start/stop/restart, no terminal needed.

### 3.3 Commands

| Command | What it does |
|---|---|
| `niq` | ensure the gateway is running in the background, then print the status table |
| `niq w [-f]` | ensure the gateway, open the browser, and print the address as a fallback |
| `niq i` | ensure the gateway and open the interactive terminal picker |
| `niq c [project]` | ensure the gateway and chat with a project in the terminal (the only project when one exists) |
| `niq status [--json\|--ids]` | the gateway and every project instance; reads the local snapshot without starting a daemon |
| `niq stop [-p]` | stop the gateway (`-p` also stops the running projects) |
| `niq restart [-p]` | restart it on the address it was started with (`-p` also restarts the running projects) |
| `niq update` | install the latest release with npm |
| `niq completion install` | put completion where your shell already looks |
| `niq completion <shell>` | print a script for bash, zsh or fish |
| `niq --version` | print the version and exit |
| `niq --help` | print usage |

`-p` / `--projects` exists because of the process model: projects outlive the
gateway, so a plain `niq stop` leaves them serving on their loopback ports.

### 3.4 Start flags

Flags go anywhere a start goes — `niq --addr :8080`, `niq w --addr :8080`,
`niq restart` reuses the recorded address.

| Flag | Default | Meaning |
|---|---|---|
| `--remote` | off | listen on **all interfaces for this run**; requires `--auth` or saved credentials |
| `--local` | off | listen on **loopback only for this run**, ignoring a remembered remote address |
| `--addr <host:port>` | remembered default, else `127.0.0.1:9527` | explicit listen address; an explicit non-loopback bind is what exposes niq |
| `--auth <user:pass>` | from `~/.niq/common/auth.json` | Basic auth for non-loopback peers; persists to that file when set |
| `--foreground`, `-f` | off | run attached to this terminal (systemd, containers, `docker run`, or just watching it) |

`niq -f` and `niq w -f` are the same thing: the foreground gateway. With no
terminal to detach from, they are what a supervisor should use.

`--remote` / `--local` are per-run switches: they change this start and nothing
else, and they keep the port (the remembered one when set, otherwise 9527).
They are mutually exclusive, and neither can be combined with `--addr` (one
address, said once). Both are scriptable substitutes for the prompt below —
for a service that should always be remote, put `--remote` in the unit file, or
answer the prompt once and let it be remembered.

#### Binding, and remembering the choice

A bare `niq --addr` is not the only way to pick the bind. Three ways exist, in
precedence order:

1. **Say it once, on the command line**: `niq --remote`, `niq --local`, or
   `niq --addr <host:port>`. Per-run, nothing is written.
2. **Remembered preference** (`~/.niq/common/config.json`).
3. **Ask**, when neither of the above applies.

So a bare `niq` with no `--addr`, no `--remote`/`--local` and no remembered
preference asks, on a terminal:

```
who may reach the gateway?
  [1] this machine only  — 127.0.0.1:9527 (loopback)
  [2] all interfaces — other machines can open the WebUI
choice [1/2, Enter=1]:
```

It then offers to remember the answer. Saying **no** is a deliberate "decide
per run": the question comes back on the next start. Saying **yes** writes
`~/.niq/common/config.json`:

```json
{ "default_addr": ":9527" }
```

Resolution order is **explicit flag > remembered preference > loopback default**,
so a one-off `niq --addr …` (or `--remote`/`--local`) never disturbs what you
saved. The gateway itself is unchanged: it still defaults to loopback, and it
only binds what the CLI passes in. Delete `config.json` (or its `default_addr`
key) to be asked again.

A remote bind requires auth. `--remote` **fails** with an explanation until
credentials exist (`niq --auth user:pass --remote` fixes it in one step);
choosing "all interfaces" at the prompt explains the same thing and stays on
loopback without remembering anything. A preference saved earlier is also
skipped — with a note — if the credentials have since been deleted. An explicit
`--addr` still binds regardless (it is a direct instruction), and the gateway
warns that it is unauthenticated.

The status line always states the exposure, so there is nothing to guess:

```
webui    http://localhost:9527  loopback only
remote   off — needs auth first: niq --auth user:pass
```

When the plane is exposed (and reachable), the second line becomes the URL a
different machine would open.

If the address is already taken, a start does not simply fail: in a terminal it
offers to stop the process holding the port, move to the next free port on the
same host, or cancel. Without a terminal (a script, a supervisor) it names the
holder and the two ways out, and exits non-zero.

### 3.5 Status for scripts

`niq status --json` prints the same snapshot the table draws:

```json
{
  "version": "0.16.0",
  "gateway": {
    "running": true,
    "pid": 4711,
    "addr": "127.0.0.1:9527",
    "url": "http://localhost:9527",
    "log": "/Users/me/.niq/niq.log",
    "started": "2026-11-01T23:57:12Z"
  },
  "projects": [
    {
      "id": "pin",
      "running": true,
      "webui": 57609,
      "bus": 57608,
      "url": "http://localhost:9527/p/pin/",
      "log": "/Users/me/.niq/projects/pin/logs/niq-2026-11-01.log"
    }
  ]
}
```

`niq status --ids` prints just the project ids, one per line. Both forms read the
local snapshot (`~/.niq/shell.json` plus the project directories), so they work
with the gateway down — and they are what the macOS app and shell completion
call.

### 3.6 Shell completion

```sh
niq completion install                     # detect the shell and write the script
niq completion install --shell zsh         # skip detection (SHELL is unset, e.g. in a script)
```

It writes into a directory the shell already scans and prints where it went:

- **zsh**: asks `zsh` for its `$fpath` and takes the best writable entry
  (Homebrew's `site-functions`, or oh-my-zsh's `custom/completions`);
- **fish**: `~/.config/fish/completions/niq.fish`;
- **bash**: `~/.local/share/bash-completion/completions/niq`.

No dotfile edit is needed. If no usable directory exists (a locked-down
`/usr/share/zsh`, a shell started from a script), print the script instead:

```sh
# bash
source <(niq completion bash)
# zsh
niq completion zsh > "${fpath[1]}/_niq"
# fish
niq completion fish | source
```

`make install`, the install script and the npm package's `postinstall` all run
`completion install`, so a normal install ends with tab completion working.
`NIQ_NO_COMPLETION=1` skips it.

### 3.7 Output

Colored when stdout is a terminal, plain otherwise (or with `NO_COLOR` set, or
`TERM=dumb`), so piping and logging stay readable.

### 3.8 Picker keys

`niq i` opens an arrow-key picker over every project — state, ports and
directory per row:

| Key | Action |
|---|---|
| `↑` / `↓`, `⌃p` / `⌃n`, `k` / `j` | move |
| `enter` | select (then chat when running, or start / stop / restart) |
| `q`, `Esc`, `⌃C` | quit |

Without a terminal the picker prints the same table instead.

### 3.9 Chat

`niq c` (or the picker's **Chat** action) opens a conversation with one project
in the terminal — the same bus the WebUI talks to, from another client. It sends
a message as the human's interface worker and prints the live event stream as it
arrives.

The view is full-screen (the terminal's alternate buffer): the transcript fills
the frame and scrolls, while the input stays pinned to the bottom. History is
paged from the project's `/api/events/before` endpoint — an initial page loads on
connect, and scrolling to the top fetches the next older page. The *worker menu*
(`⌃T`) scopes the transcript to one worker (any worker, including tools) and, for
a talk partner, also aims the next message at it; **all** clears the filter. Start
and stop the project from the picker or the WebUI.

The scroll window and the mouse selection are standalone components: a small
`viewport` owns the content length, visible height, first line and whether it
follows the bottom, and a `selection` maps cell ranges on rendered lines to
clipboard text. Every scroll path — page keys, line keys, the wheel and a drag
past either edge — funnels through the viewport, so paging and history loading
behave the same however they were triggered.

The human's own messages stay inline on the `you › …` line and are the one block
set off by a blank line above them. A worker's answer opens with a rule carrying
its name at the very left edge (`niq ··········`), a blank row, then the body as
rendered markdown — headings, bold/italic, inline code, links, lists, quotes,
fenced code (syntax-highlighted for go, python, js/ts, json, shell and rust), pipe
tables and rules — wrapped flush left. Tables are fitted to the terminal width,
shrinking and wrapping cells rather than overflowing. Context is compact: a
*thinking* trace is one grey row — a coloured label, a colon, then a dim detail
truncated with an ellipsis rather than wrapped. A tool call is its own card
instead, in pi's shape: the first line is the call — the capability, the worker
it addressed, then the argument payload, ellipsized when it does not fit —
followed by a blank spacer and at most two rows of the reply in a softer tone.
The answer is folded into the call, so one call and its reply are one card, never
a stripe of rows; only the human's `you ›` and a worker's `id ›` keep the arrow,
and plain event rows use the `label:` form.

Titles are colour-coded so the transcript can be scanned: the human's `you` is
green, a worker's answer keeps the brand blue, `thinking` is grey, warnings
(`interrupted`, `abort`, `timeout`, `cancel`) are bright yellow on their own
amber surface, and failures are red on the error surface.

Structure comes from surfaces, as in pi: the human's turns sit on a tinted card
(no underline; injected `<system-reminder>` blocks are hidden), each tool call is
its own green card — a bright call line, a spacer, then the reply in a lighter
tone, on a deep desaturated green — and code blocks sit on a code surface, with
the input framed by two rules with a blank row above it (the composer has no
surface of its own). A worker's
answer takes no surface of its own; its rule starts at the left edge and its body
is inset by one cell, so the prose lines up with the text inside those cards.
Thinking is
not boxed: it plays in grey, up to ten lines per trace, streaming as it is
produced — past ten it keeps the first three lines and the newest seven with a
`··· N lines omitted ···` row between them. Each surface insets
its text and pads a row above and below, and one blank row separates adjacent
blocks, so the transcript reads in layers rather than a wall of text. The target,
view scope,
scroll position and keys are drawn into the dock's top rule (`── … ──`), visible
without costing the transcript a row. Bands downgrade to 256 colours, or disappear
under `NO_COLOR` / a 16-colour terminal. The palette follows the terminal rather
than a guess: on start niq asks for the background colour (OSC 11) and the colour
scheme (`CSI ? 997 n`), and subscribes to scheme changes (mode 2031), so a light
terminal gets the light surfaces and toggling the theme repaints live. Terminals
that answer none of it fall back to `COLORFGBG`, then to dark. The
wheel scrolls one line per notch; each sent message records its addressee
(`you → <id> › …`), and a dropped stream is retried on a capped backoff with its
status shown in the rule.

Because the view owns the mouse (wheel and drag reports), the terminal's own
selection is off inside it. Dragging over the transcript selects text there and
releasing copies it — OSC 52 for terminals that support it, plus `pbcopy` on
macOS or `wl-copy`/`xclip`/`xsel` on Linux when present. Selection is drawn as
reverse video and survives scrolling.

The composer is its own component, and it takes the same shapes the WebUI's input
does. It is multi-line: `shift+enter` (where the terminal speaks the Kitty
keyboard protocol), `⌃J`, or `⌥enter` starts a new line and `enter` sends. A large
paste stays one chip on screen — `[pasted text #1 · 24 lines]` — and expands to
its real text only when the message is sent, so pasting a stack trace does not
bury the conversation or fight the cursor. Pasting a file path (or dropping a file
onto the terminal, which most terminals paste as a path) stages it as an
attachment: images ride inline as base64, other files are referenced by their
path or uploaded, depending on where the gateway runs (below). Attachments and
long messages show above the input, capped, and the dock scrolls to keep the
cursor visible. `⌃X` opens the input-mode picker (default / append / interrupt /
schedule), the same `input_mode` the WebUI's mode picker sends; the current mode
and attachment count show in the dock's top rule.

A `file` block is only a *reference*: the reason worker turns it into a note
naming the path (`[referenced file: … (N bytes)]`) and leaves the bytes alone —
what reads it is a tool, running where the project runs. So the path has to be
valid on the gateway's machine, which is exactly why locality matters. When the
gateway is on this machine (a loopback address, or its hostname) the attachment
keeps its local path: no copy, and a tool can open it directly. When it is not,
the file is uploaded through `POST /api/upload` before the message is sent and the
path the gateway returns is what the message names; the dock shows `(upload)` so
the difference is visible. `NIQ_ATTACH=upload` or `NIQ_ATTACH=path` overrides the
decision — an SSH tunnel to a remote gateway looks local, and a shared mount makes
a remote host behave like a local one. Images are the exception either way: they
ride inline as base64, which is a real multimodal block the model sees.

| Key | Action |
|---|---|
| `q` / `⌃C` | quit (`Esc` is ignored, so an IME cancelling a composition cannot close it) |

The conversation view is modal, like vim. It opens in **insert mode**, where every
key goes to the message; `esc` switches to **normal mode**, where keys scroll the
transcript; `i` (or a click in the composer) goes back. The rule above the input
has the send target, the view scope and the input mode on the left, and the vim
state plus a `help` entry on the right (shortcut reminders live in the help card,
not the rule). Every segment of that rule is a click target: `target:`, `view:`
and `mode:` open the matching picker, `help` opens the keymap, and clicking the vim
state toggles insert / normal. `?` in normal mode or `⌃?` in either mode also opens
the keymap. Every popup closes on `esc` or a click outside it (a card closing never
tears down the conversation, so `⌃C` still needs a second press to leave).

| insert mode | Action |
|---|---|
| `enter` | send the message |
| `shift+enter`, `⌃J`, `⌥enter` | insert a newline (multi-line message) |
| `←` / `→` / `↑` / `↓` | move the cursor |
| `⌃A` / `⌃E` | line start / end |
| `⌃B` / `⌃F`, `⌥b` / `⌥f` | char left / right; by word |
| `⌃P` / `⌃N` | previous / next line |
| `⌫` / `⌦` | delete before / after the cursor |
| `⌃K` | kill to the end of the line |
| `⌃W` | kill the previous word |
| `⌃Y` | yank the last kill |
| `⌃?` | keybinding help |
| `esc` | normal mode |

| normal mode | Action |
|---|---|
| `j` / `k`, `⌃E` / `⌃Y` | scroll one line down / up |
| `⌃D` / `⌃U`, `⌃F` / `⌃B`, `PgUp` / `PgDn`, `⌃↑` / `⌃↓` | page the transcript (reaching the top loads more history) |
| `g` / `G` | jump to the oldest / newest |
| `i` | insert mode |
| `?` | keybinding help (a card over the transcript; `esc`, any keyboard key, or a click outside closes it, `⌃C` included) |
| `shift+↑` / `shift+↓` | scroll one line |

| both modes | Action |
|---|---|
| `⌃X` | input-mode picker: default / append / interrupt / schedule |
| `⌃T` / `⌃V` | target / view pickers, scoped like the WebUI to reason/coding/niq workers (`↑`/`↓`, `j`/`k`, `⌃P`/`⌃N`, `enter`, `esc`; each scrolls when long) |
| mouse wheel | scroll one line per notch |
| mouse drag | select text; release copies it |
| `⌃C`, `⌃Q` | leave the conversation |

## 4. Server deployment

The gateway is the only port a deployment needs to expose: it serves the SPA,
the project-management API and every project's WebUI (at `/p/<id>/`). Each
project's HTTP API binds loopback only, so it stays unreachable from outside
even on a public machine; the event bus listens on all interfaces but
authenticates every connection with a signed worker token.

```sh
niq --addr :9527 --auth alice:s3cret
```

On a terminal you can skip the flags: a bare `niq` asks whether to bind loopback
or all interfaces, and offers to remember the answer (see §3.4). The first start
with no credentials still needs auth, so `--auth` (or a hand-written
`~/.niq/common/auth.json`) is the one thing that cannot be skipped — the prompt
tells you so and stays on loopback until it exists.

Auth is **opt-in**. With credentials configured, requests from any non-loopback
address are answered with a 401 until they authenticate, while requests from the
machine itself stay password-free. `--auth` persists to
`~/.niq/common/auth.json`, so one pair of credentials covers the gateway and
everything it proxies.

With no credentials configured, the server enforces nothing: every caller that
can reach it is served, and the gateway says so at startup. That is why it
listens on loopback by default — pass an explicit `--addr` (and, ideally,
`--auth`) to accept remote connections.

Basic auth sends the credentials on every request, so terminate TLS in front (or
tunnel through SSH/WireGuard) before exposing niq to the open internet — niq
itself speaks plain HTTP.

To keep it attached to a terminal instead of detached — systemd, a container,
`docker run`, or just watching the log:

```sh
niq w --foreground
```

## 5. Environment variables

| Variable | Used by | Effect |
|---|---|---|
| `NIQ_HOME` | everything | relocate the root (default `~/.niq`); `~`/`~/` are expanded |
| `NIQ_AUTH_CONFIG` | gateway | override `common/auth.json` wholesale |
| `NIQ_PROVIDER_CONFIG` | launcher, project, workers | override `common/providers/provider.json` wholesale |
| `NIQ_NO_UPDATE_CHECK=1` | `niq` | disable the daily registry check |
| `NIQ_NO_COMPLETION=1` | installers, `niq completion install` | skip completion |
| `NIQ_NO_APP=1` | npm `postinstall` | do not copy `niq.app` to `~/Applications` |
| `NIQ_INSTALL_DIR`, `NIQ_VERSION`, `NIQ_NO_MODIFY_PATH` | install script | see [§1](#install-script-no-npm) |
| `PREFIX` | `make install` | symlink target directory (default `~/.local/bin`) |
| `NO_COLOR`, `TERM=dumb` | every command | plain output |

## 6. Where it writes

Everything lives under `~/.niq` unless `NIQ_HOME` says otherwise:

```
~/.niq/
  shell.json                   gateway snapshot (pid, address) that `niq status` reads
  niq.log                      gateway log
  update-check.json            cached "is there a newer release" answer
  bin/niq                      install script / npm: the binary a GUI launch can find
  common/auth.json             Basic auth credentials (gateway-owned)
  common/templates/            project templates
  common/providers/provider.json   shared provider config (API keys via env expansion)
  projects/<id>/               one directory per project
```

`--auth` writes `common/auth.json`; the gateway never writes project files, and
a project only writes its own state, logs, and identity registry. The full
runtime layout is documented in `design/project/README.zh-CN.md`.
