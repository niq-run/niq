# niq v0.15.0

A release about reach: the CLI got a short name and a machine-readable status,
and niq now has a macOS menu bar app that shows the same thing and does the same
things — built out of the CLI rather than beside it.

## Highlights

- **`miq`, a menu bar app.** One click: open the control plane's WebUI, stop it,
  and one entry per project with open / start / stop / restart. macOS only, and
  it ships with the release as a universal build.
- **`niq p`** is `niq project`, and **`niq status --json`** is the report as data.
- **`niq project start|stop|restart <id>`** — the picker's three actions without
  the picker.
- **Completion with no dotfile edit.** `niq completion install` writes into a
  directory your shell already scans, and the installers call it.

## 1. `niq p`

The picker is the command people run most, so it has a one-letter spelling:
`niq p` is `niq project` word for word — `p list`, `p create <id>`, `p run <id>`,
and the picker on its own. Both words dispatch from the same case in `main`, so
they cannot drift.

## 2. `niq status --json`

```
$ niq status --json
{
  "version": "0.15.0",
  "control": {
    "running": true,
    "pid": 99561,
    "addr": ":9527",
    "url": "http://localhost:9527",
    "log": "~/.niq/niq.log",
    "started": "2026-11-03T13:22:28+08:00"
  },
  "projects": [
    {
      "id": "pin",
      "running": true,
      "webui": 57609,
      "bus": 57608,
      "url": "http://localhost:9527/p/pin/",
      "log": "~/.niq/projects/pin/logs/niq-2026-11-03.log"
    }
  ]
}
```

A stopped control plane is not an error: the projects keep their ports and can be
started from that answer. `url` is the proxy while the plane is up and the
loopback address otherwise, so it is always the one that works.

## 3. One project at a time

`niq project start|stop|restart <id>` are the picker's operations with an id
instead of a cursor — the same functions, so the same behaviour: wait for the
port to free before starting, snapshot on the way out, one line of report.

## 4. The menu bar app

```sh
make build-miq   # dist/niq.app and ./bin/miq
miq              # puts niq in the menu bar; running it again focuses it
```

The menu is the actions, nothing else: **Open WebUI** and **Stop** (**Start**
when the plane is down), then one entry per project — `pin · running` — each a
submenu of **Open WebUI / Start / Stop / Restart**, greyed out when the instance
cannot perform them. **Open GitHub** and **Quit** close it. It rebuilds itself
when the menu opens.

It is a front end, not a second implementation: the status is `niq status
--json`, every action is a `niq` command, and `miq` hands the app the `niq`
binary next to it, because a LaunchServices start inherits none of the shell's
PATH.

The icon is the web UI's logo — the 品 of three squares — as a template image: a
tile in the menu bar's text colour with the squares knocked out, so macOS draws
it white on a dark bar and black on a light one.

## 5. Completion

`niq completion install` writes the script into a directory the shell already
scans — a writable entry of zsh's `$fpath` (preferring one under your home, such
as oh-my-zsh's `custom/completions`), `~/.config/fish/completions`, or
bash-completion's user directory — and prints where it went.

`make install`, `install.sh` and the npm package's `postinstall` all call it.
Nothing edits `~/.zshrc`; `NIQ_NO_COMPLETION=1` skips it.

## 6. Build

The two things this repository builds have names:

```sh
make build                    # everything this machine can build
make build-niq                # the CLI (rebuilds the SPA first)
SKIP_WEBUI=1 make build-niq   # Go only, no SPA rebuild
make build-miq                # the menu bar app
make install                  # links what is already built — it does not build
```

`make install` stops with "run 'make build' first" rather than quietly starting
a minutes-long npm/vite run.

## Upgrade

```
niq update          # npm install: installs 0.15.0
niq restart -p      # put the control plane and the running projects on it
```

The menu bar app is a separate download —
`niq_menubar_darwin_universal.zip` on the release page, holding `niq.app` and
`miq`. It is ad-hoc signed but not notarized: the first launch needs
right-click → Open.

## Verification

`go build ./...`, `go vet ./app/...` and `go test ./app/...` pass, with new tests
for the picker's redraw height, the cursor skipping disabled actions, and the
version comparison behind `niq update`. Exercised on macOS: completion installed
into a fresh zsh (`zsh -ic 'echo ${_comps[niq]}'` → `_niq`), `niq p` variants,
`status --json` with the plane up and down, per-project start/stop, the menu bar
app driven through its menu, and `make dist-miq` (universal build, signature
verified, zip inspected).
