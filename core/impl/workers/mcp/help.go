package mcp

import (
	"github.com/niq-run/niq/core/impl/baseworker"
	"github.com/niq-run/niq/core/itfs/event"
)

// registerHelp declares worker.help on the mcp worker. Every fixed event gets a
// static usage guide: the way an MCP event is used does not depend on which
// server or tool is involved, so params never changes the answer. Ready-mode
// tools fall back to the generic contract-derived guide, which carries the
// remote tool's own description and argument schema.
func (w *Worker) registerHelp() {
	w.RegisterHelp(map[event.EventType]baseworker.HelpFunc{
		EvtServerList:   staticHelp(serverListHelp),
		EvtServerAdd:    staticHelp(serverAddHelp),
		EvtServerRemove: staticHelp(serverRemoveHelp),
		EvtServerUpdate: staticHelp(serverUpdateHelp),
		EvtToolList:     staticHelp(toolListHelp),
		EvtToolCall:     staticHelp(toolCallHelp),
		EvtExpose:       staticHelp(exposeHelp),
	})
}

// staticHelp adapts a fixed usage guide to HelpFunc.
func staticHelp(text string) baseworker.HelpFunc {
	return func(map[string]any) (string, bool) { return text, true }
}

const serverListHelp = `# mcp.server.list

List the remote MCP servers this worker fronts: name, url, protocol, status
(configured or connected), and cached tool count.

Request: {} — no arguments.

Start here when you do not know which servers are available, then:
- mcp.tool.list {server} — that server's tools and input schemas;
- mcp.tool.call {server, tool, arguments} — call one.`

const serverAddHelp = `# mcp.server.add

Add and connect a remote MCP server at runtime. The change is persisted and the
server's tools become callable; mcp.tool.list then shows them.

` + "```json" + `
{"name": "github", "url": "https://api.example.com/mcp", "protocol": "streamable-http", "headers": {"Authorization": "Bearer ${GITHUB_TOKEN}"}}
` + "```" + `

- name (required) and url (required) — identity and endpoint.
- protocol (optional) — remote transport.
- headers (optional) — values may reference ${ENV_VAR}; the project resolves
  them.

Notes:
- The name must be unique; adding an existing name fails.
- Adding a server is privileged: it connects to an arbitrary URL and turns that
  server's tools into callable capabilities.
- An unreachable server rolls the add back and returns the connection error.`

const serverRemoveHelp = `# mcp.server.remove

Remove a configured server, disconnect it, and withdraw its tools. Persisted.

` + "```json" + `
{"name": "github"}
` + "```" + `

- name (required) — the server to remove. An unknown name fails.
- In ready exposure mode its mcp.<server>.<tool> capabilities disappear with it.`

const serverUpdateHelp = `# mcp.server.update

Change a configured server's endpoint or credentials, reconnect it, and re-list
its tools. Persisted.

` + "```json" + `
{"name": "github", "url": "https://new.example.com/mcp", "headers": {"Authorization": "Bearer ${GITHUB_TOKEN}"}}
` + "```" + `

- name (required) — which server to update.
- url / protocol / headers (optional) — only the fields you provide change;
  headers replace the previous map rather than merging.
- The server reconnects and its tool cache refreshes in the background.`

const toolListHelp = `# mcp.tool.list

List one server's tools: name, description, input schema, and (when declared)
the output schema a caller can plan its parse against.

` + "```json" + `
{"server": "<name from mcp.server.list>"}
` + "```" + `

## Workflow

1. mcp.server.list {} — which servers exist.
2. mcp.tool.list {server} — which tools it exposes and how to call them.
3. mcp.tool.call {server, tool, arguments} — call one.

## Notes

- In ready exposure mode each tool is also its own capability
  (mcp.<server>.<tool>, names sanitized), so discovery is optional for a tool
  you already know.
- Tools absent from this list are not callable; an allowed_tools allowlist may
  narrow the surface.
- The list is cached from the server; a reconnect or mcp.server.update
  refreshes it.`

const toolCallHelp = `# mcp.tool.call

Call one tool on a remote MCP server.

` + "```json" + `
{"server": "github", "tool": "search_issues", "arguments": {"query": "open bugs"}}
` + "```" + `

- server — a name from mcp.server.list.
- tool — a tool name from mcp.tool.list for that server.
- arguments — an object matching the remote tool's input schema. Fetch it with
  mcp.tool.list first; in ready mode the flattened capability carries the same
  schema as its parameters.

## Notes

- The reply is the remote tool's content as text; structured output, when the
  server declares it, follows the text as JSON.
- A dropped session reconnects on the next call.
- Tools hidden by an allowed_tools allowlist are rejected as not allowed by
  config.`

const exposeHelp = `# mcp.expose

Read or switch the exposure mode. Persisted.

- {} — read the current mode.
- {"mode": "agentic"} or {"mode": "ready"} — switch.

## Modes

- agentic — fixed discovery surface: mcp.server.list, mcp.tool.list,
  mcp.tool.call. Remote tools are not individual capabilities.
- ready — the discovery surface plus one capability per remote tool
  (mcp.<server>.<tool>, names sanitized), so callers can invoke them directly.

Fixed-mode workers do not register this event.`
