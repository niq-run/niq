# `Sources/Features` — one folder per surface

Each subfolder is a screen of the window, mirroring a WebUI view. They read
`AppModel` from the environment and never touch `GatewayClient` directly.

| Folder | Surface | WebUI counterpart | Status |
|---|---|---|---|
| `Talk/` | conversation: message cards, folded process runs, usage, composer | Talk | full rendering + composer |
| `Events/` | raw event inspector: search, type filter, trace filter, payload | Events | list + filter + inspector |
| `Approvals/` | the approver's queue: payload, 批准 / 拒绝 | Approvals | list + decide |
| `Workers/` | roster + create dialog + detail (info/readme/context/events/perms) | Workers | full |
| `Mcp/` | MCP worker rail + server/tool management | MCP | read + add/remove/expose |
| `Programs/` | programs: Markdown detail, create/edit/delete | Programs | full |
| `Projects/` | project catalog: create/link/start/stop/restart | Projects | full |
| `Templates/` | template catalog: JSON edit, create/clone, import, delete | Templates | full |
| `Models/` | model providers: visual read + JSON write | Providers | read + JSON edit |
| `Settings/` | gateway address / binary | — | minimal |

Adding a surface = one folder + one case in `AppModel.Section` + one line in
`Shell`'s switch. Shared visual pieces live in `Sources/Design` (`Niq` tokens,
`Pill`, `OutlineButton`, `Chip`, `Dot`, `SectionBox`, `FormRow`,
`SquareTextEditor`, `SquareField`, `SquareTabs`, `PageHeader`).

**Style rule.** Content and chrome are square (1px borders, no rounded cards),
the WebUI's language. Native controls are used where the platform expectation is
stronger: `.switch` toggles, `Menu`, `.popover`, `.sheet`, `NSAlert`,
`NSOpenPanel`.
