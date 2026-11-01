# niq

niq is an **event-driven agent runtime**. It is a single agent composed of many **Workers** that collaborate over an event bus — each worker handles its own focus, and together they form the complete agent.

niq has exactly three core concepts:

- **Worker** — a unit that can do things, like an **actor**: it has its own state and communicates only by sending and reacting to messages. A Worker subscribes to events, processes them, and publishes new events. The Worker is niq's only extension point.
- **Program** — the source code of a Worker's capability: a natural-language prompt (like the skills of mainstream agents) or a DSL script (like programmatic tool calling, PTC). Both forms are source code for generating tool calls.
- **Event** — the only communication language between Workers, delivered over the event bus.

> **Collaboration is extension.**

niq grows not by adding new abstractions, but by adding more participants that cooperate over the event bus. Every capability (reasoning, tool execution, safety guards, lifecycle) is a Worker. Extending a capability and having a human join the collaboration are the same act — a user is just another collaborating participant, and there can be one or many of them. In niq, everything is workers collaborating over the event bus.

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Install

**npm** (recommended):

```sh
npm install -g @niq.run/niq
```

**Install script** (installs to `~/.niq/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/app/publish/install.sh | sh
```

**From source** (Go 1.26+ — the embedded JS engine requires it):

```sh
git clone https://github.com/niq-run/niq
cd niq && make build   # → ./bin/niq
```

**Runtime: none to install.** The program worker runs script programs (PTC)
inside an embedded QuickJS engine (`modernc.org/quickjs`, pure Go), so there is no
`node` process to spawn and no JS runtime to install. Scripts see only the SDK the
worker injects: no `process`, `require`, `fetch`, or dynamic `import()`.

## Quick start

niq has two layers: a **control plane**, and **projects** — each project is an isolated agent instance with its own event bus, worker set and WebUI on its own ports.

```sh
# start the control plane (default :9527) in the background
niq
```

`niq` starts the control plane detached from the terminal, prints where it is
reachable, and returns to the prompt — closing the shell (or losing an SSH
session) no longer stops it. Running `niq` again reports on the service instead
(it is exactly `niq status`), so one command covers both "start it" and "is it
up?".

```
 - niq -  version: 0.14.0

[control] running  pid 4711 · since 2026-11-01 23:57
webui    http://localhost:9527
log      ~/.niq/niq.log
stop     niq stop
restart  niq restart

[projects]  niq project to start, stop or restart one
● pin   running  webui 57609  bus 57608  http://localhost:9527/p/pin/
○ notes stopped  webui 57621  bus 57620  http://localhost:9527/p/notes/
```

`niq status` reports the process, address and log, plus each project's run
state and URL (a refused port means that instance is not running); `niq stop`
shuts it down — projects are their own processes, so they keep serving unless
you add `-p`;
`niq restart` brings it back on the same address. Put the flags anywhere a
start goes (`niq --addr :8080 --auth alice:s3cret`, `niq restart` reuses the
recorded address). To keep it attached to the terminal — systemd, a container,
`docker run`, or just to watch it — pass `--foreground` (or `-f`):

```sh
niq control --foreground
```

Open **http://localhost:9527** — the control plane's WebUI lists your projects and can create and start one from a template, no terminal needed.

Prefer the CLI? Both steps can also be done manually:

```sh
niq project create my-project
niq project run my-project
```

`niq project` on its own (or `niq project list`) is an arrow-key picker over
every project — state, ports and directory per row: `↑`/`↓`, `⌃n`/`⌃p` or `j`/`k`
to move, `enter` to pick, then start (if stopped) or stop / restart (if running). An instance is
identified by its ports, so the CLI, the WebUI and a project started by hand in
a terminal all act on the same thing — the control plane keeps no list of what
it launched. Without a terminal the picker prints the same table; `--ids` prints
just the ids.

| command | |
| --- | --- |
| `niq` | start the control plane in the background, or report on it when it is already running |
| `niq -f`, `niq --foreground` | run the control plane in this terminal |
| `niq status` | the control plane and every project instance |
| `niq stop [-p]` | stop the control plane (`-p` also stops the running projects) |
| `niq restart [-p]` | restart it on the address it was started with (`-p` also restarts the running projects) |
| `niq project` | pick a project: start, stop or restart it |
| `niq project create` / `niq project run` | create a project / run one in this terminal |
| `niq completion <shell>` | completion for bash, zsh or fish |

```sh
# bash
source <(niq completion bash)
# zsh
niq completion zsh > "${fpath[1]}/_niq"
# fish
niq completion fish | source
```

Output is colored when it goes to a terminal and plain otherwise (or with
`NO_COLOR` set), so piping and logging stay readable.

Each project serves its own WebUI on a loopback-only port, so it is never
exposed by accident: open it through the control plane at
**http://localhost:9527/p/&lt;id&gt;/**, which proxies to that project. That address
works whether or not the instance is running — opening a stopped project's
address lands on the projects list with a start prompt instead of an error
page. Ports are
assigned dynamically on first run and then persisted per project, so they stay
stable across restarts (`--bus` / `--webui` override). `niq project list` shows
each project's `webui` and `bus` ports.

### Server deployment

The control plane is the only port a deployment needs to expose: it serves the
SPA, the project-management API and every project's WebUI (at `/p/<id>/`).
Projects and the event bus bind loopback only, so they stay unreachable from
outside even on a public machine.

```sh
niq --addr :9527 --auth alice:s3cret
```

Configure credentials before exposing the port: requests from any non-loopback
address are then answered with a 401 until they authenticate, while requests from
the machine itself stay password-free. `--auth` (control plane) and
`--webui-auth` (a project started by hand) persist to and read from the same
`~/.niq/common/auth.json`, so one pair of credentials covers everything. Without
credentials remote callers are locked out entirely, and niq warns about the
binding at startup.

Basic auth sends the credentials on every request, so terminate TLS in front (or
tunnel through SSH/WireGuard) before exposing niq to the open internet — niq
itself speaks plain HTTP.

After upgrading the binary, `niq restart` the control plane and restart the
running projects: every process serves the page and assets it was built with
(asset URLs are relative, so a project page loads its own bundle through
`/p/<id>/`), which means an older project keeps rendering the older UI until it
is restarted.

### Model provider

The `reason` worker talks to an LLM provider. Provider settings (endpoint, model, API key) live in `~/.niq/common/providers/provider.json`; `api_key` and header values support environment-variable expansion — e.g. `"api_key": "${OPENAI_API_KEY}"` keeps secrets out of stored files.

## Project layout

```
core/itfs/ interfaces & types (contracts, not implementations)
core/impl/ implementations (workers, bus, transports, providers, hosts)
app/       the niq product: control plane, project runtime, WebUI
app/publish/cmd  CLI entry point (package main)
app/publish/npm  npm distribution (launcher shim + package template)
ext/       external worker implementations (HTTP)
```

## Build & test

```sh
go build ./... && go vet ./...
go test ./core/impl/eventbus/ -count=1
```

The WebUI bundle is committed (it is `go:embed`-ed), so rebuild and commit it
after changing anything under `app/webui/assets/src`:

```sh
cd app/webui/assets && npm run build
```

### Frontend development

With the control plane running, the Vite dev server (`npm run dev` in
`app/webui/assets`, port 5173) proxies API traffic to :9527, so both pages
work with HMR:

- control plane UI: http://localhost:5173/
- a project's UI: http://localhost:5173/p/&lt;id&gt;/ (that project must be running)

## Data directory

Runtime data lives under `~/.niq/` by default. Set the `NIQ_HOME` environment
variable to relocate this root to a custom directory (for a portable install,
tests, or to keep state out of your home dir); `~`/`~/` in the value are
expanded. `NIQ_AUTH_CONFIG` and `NIQ_PROVIDER_CONFIG` still override those two
individual files wholesale, taking priority over `NIQ_HOME`.

```
~/.niq/
  bin/                         prebuilt binary (install.sh)
  niq.log                      control-plane log
  common/
    templates/                 project templates
    providers/provider.json    shared provider config (API keys via env expansion)
  projects/<id>/               one directory per project
    id/identities.json         worker identity registry (owned by the bus)
    programs/                  Program storage
    logs/                      daily-rotated run logs
```
