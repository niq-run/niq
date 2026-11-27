# niq

**English** · [简体中文](doc/README.zh-CN.md)

niq is an **event-driven agent runtime**. It is a single agent composed of many **Workers** that collaborate over an event bus — each worker handles its own focus, and together they form the complete agent.

niq has exactly three core concepts:

- **Worker** — a unit that can do things, like an **actor**: it has its own state and communicates only by sending and reacting to messages. A Worker subscribes to events, processes them, and publishes new events. The Worker is niq's only extension point.
- **Program** — the source code of a Worker's capability: a natural-language prompt (like the skills of mainstream agents) or a DSL script (like programmatic tool calling, PTC). Both forms are source code for generating tool calls.
- **Event** — the only communication language between Workers, delivered over the event bus.

> **Collaboration is extension.**

niq grows not by adding new abstractions, but by adding more participants that cooperate over the event bus. Every capability (reasoning, tool execution, safety guards, lifecycle) is a Worker. Extending a capability and having a human join the collaboration are the same act — a user is just another collaborating participant, and there can be one or many of them. In niq, everything is workers collaborating over the event bus.

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Get started

`niq` has one entry point: install it, then run it.

```sh
npm install -g @niq.run/niq   # or: the install script, or a source build
niq                           # starts the gateway in the background and reports
```

That prints the gateway's address, its log, and every project with its ports —
then gets out of the way. From there, **http://localhost:9527** is the WebUI, and
`niq i` is the same thing in the terminal.

If the gateway's port is already taken, `niq` asks instead of failing: stop the
process holding it, start on another port, or cancel. With no terminal (a script)
it reports who holds the port and the two ways out.

Installation (three channels), upgrading, every command and flag, server
deployment and the environment variables are all in
**[doc/install-and-cli.md](doc/install-and-cli.md)**.

## Model provider

The `reason` worker talks to an LLM provider. Provider settings (endpoint, model, API key) live in `~/.niq/common/providers/provider.json`; `api_key` and header values support environment-variable expansion — e.g. `"api_key": "${OPENAI_API_KEY}"` keeps secrets out of stored files.

## Project layout

`core/` holds contracts and the mechanisms that do not depend on a deployment
form; `app/` holds the forms and the product entry, with `app/client/` for the
interfaces and `app/publish/` for the distributions.

[doc/directory-layout.md](doc/directory-layout.md) is the full picture: the
as-built tree, the ownership rules behind it, where new things go, and the
dependency checks that enforce them.

## Build & test

Go alone builds, vets and tests the backend — the WebUI bundle is committed and
`go:embed`-ed, so no Node is needed to compile the CLI:

```sh
go build ./... && go vet ./...
go test ./...            # ~6 s
make build               # ./bin/niq (the desktop shell is `make desktop`)
```

[doc/build-and-test.md](doc/build-and-test.md) has the make targets, how to run
one package or one test, the WebUI dev server, the TypeScript SDK, the local
release dry run, and what is (and is not yet) covered.

## Data directory

Everything niq writes lives under `~/.niq` (`NIQ_HOME` relocates the root;
`NIQ_AUTH_CONFIG` / `NIQ_PROVIDER_CONFIG` override those two files wholesale).
The tree, which component owns each file, and what each process reads are in
[doc/install-and-cli.md §6](doc/install-and-cli.md#6-where-it-writes).

## License

MIT — see [LICENSE](LICENSE).
