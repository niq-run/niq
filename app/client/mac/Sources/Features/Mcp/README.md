# `Features/Mcp` — MCP workers

An MCP worker is an ordinary worker that proxies remote MCP servers' tools onto
the bus. There is no MCP-specific backend: `McpView` drives the worker with its
own `mcp.*` events through the generic ask path (`POST .../workers/{id}/event`
then poll `.../events/by-request/{rid}`).

- **Rail:** the project's `type == "mcp"` workers, each with its declared mode
  (固定 / 动态) and declared server count (from `params`).
- **Panel:**
  - 声明 — mode, exposure (`agentic` / `ready`), allowed tools, from `params`
    (including the legacy nested `params.mcp` form, flat keys winning).
  - 服务器 — `mcp.server.list` (name · url · status · tool count); per row 工具
    expands `mcp.tool.list`; 移除 calls `mcp.server.remove`.
  - 添加服务器 — name + `http(s)` URL + protocol + `KEY=VALUE` headers →
    `mcp.server.add`.
  - Exposure switch → `mcp.expose {mode}`; `mcp.expose {}` reads it.

A `fixed` declaration is configuration-only, so all mutation controls are hidden
for it (matching the WebUI).

`MCPServer`, `MCPTool` and `MCPDeclaration` at the bottom of the file are the
defensive parsers for the reply shapes and the params bag.
