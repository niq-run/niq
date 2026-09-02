# @niq.run/niq

> **niq runs programs that haven't been written yet.**

niq is an **event-driven agent runtime** — a single agent composed of many **Workers** that collaborate over an event bus. Each worker handles its own focus; together they form the complete agent.

- **Worker** — an actor-like unit with its own state that communicates only through events
- **Program** — the source of a Worker's capability: a natural-language prompt (like the skills of mainstream agents) or a DSL script (like programmatic tool calling, PTC) — both are source for generating tool calls
- **Event** — the only communication language between Workers

> **Status: early, fast-moving development.** The design is still evolving; APIs and behavior may change without notice.

## Install

```sh
npm install -g @niq.run/niq
```

Requires Node.js ≥ 14. The native binary for your platform is installed automatically as an npm optional dependency — **no postinstall scripts, no downloads at setup time**, and it works in offline / air-gapped environments.

Supported platforms: macOS (arm64, x64) · Linux (arm64, x64) · Windows (arm64, x64)

## Usage

```sh
niq                # start niq with the default preset
niq --version
```

Once started, open the WebUI (URL is printed in the terminal) to create and run projects, chat, and inspect the event flow.

## How this package works

This package is a thin Node.js launcher. The actual Go binary ships in per-platform packages that npm installs automatically via `optionalDependencies`:

| Package | Contents |
|---|---|
| `@niq.run/niq` | launcher shim (this package) |
| `@niq.run/niq-darwin-arm64` | macOS Apple Silicon binary |
| `@niq.run/niq-darwin-x64` | macOS Intel binary |
| `@niq.run/niq-linux-arm64` | Linux ARM64 binary |
| `@niq.run/niq-linux-x64` | Linux x64 binary |
| `@niq.run/niq-win32-arm64` | Windows ARM64 binary |
| `@niq.run/niq-win32-x64` | Windows x64 binary |

Only the package matching your platform is unpacked; the rest are skipped by npm. The launcher resolves the installed binary at runtime and forwards all arguments to it.

## Links

- Repository: <https://github.com/niq-run/niq>
- Issues: <https://github.com/niq-run/niq/issues>

## License

MIT
