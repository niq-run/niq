# `Features/Workers` — roster, creation, and the detail panel

## Roster (`WorkersView`)

The WebUI table: **工作单元 ID · 备注名 · 类型 · 描述 · 连接 · 生命周期 · 托管**,
with search, a type filter, a 显示已归档 switch, and 创建工作单元. Row context
menu: 查看详情 / 归档. Archived workers are hidden unless the switch is on.

## Create (`CreateWorkerDialog`)

Mirrors the WebUI's body exactly:
`{type, id, placement, label?, tags?, description?, params?, command?, cwd?,
token_ttl_seconds?}`.

- 托管 (`in-process`), 子进程 (`managed-process`), 三方 (`unmanaged-process`).
- Managed params by type: reason/coding (instruction, goal, brief, readme,
  provider, model, tool_mode), workspace/program (mounts), mcp (servers JSON,
  expose, fixed, allowed_tools), niq (noted as params-to-edit-afterwards).
- Subprocess: command, cwd, and `KEY=VALUE` env rows (which become `params`).
- Remote: an optional credential TTL in days.

## Detail (`WorkerDetailView`, a sheet)

Tabs: **信息 / 自述 / 上下文 / 事件 / 许可**.

- **信息:** identity, connection, lifecycle, placement, tags, credential (with
  copy); inline editing of label/tags/description (`PUT .../meta`); the `params`
  bag with a JSON editor (`PUT .../params`, applies on restart).
- **自述:** `worker.readme` via the ask path, rendered as Markdown.
- **上下文:** provider/model switch (`GET|POST .../provider[s]`), the
  `context.status` accounting with the shared usage panel, and — for workspace
  workers — the mount list with add/remove (`.../mounts/{add|remove}`).
- **事件:** the worker's recent events; clicking one jumps to its trace.
- **许可:** `publish_allow` / `subscribe_allow` row editors
  (`PUT .../allow`).

Header: 启动 / 休眠 / 唤醒 / 重启 / 停止 (gated by state and placement), 归档,
删除 (confirmed).
