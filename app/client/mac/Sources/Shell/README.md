# `Sources/Shell` — the window shell

`ContentView` is a **two-column HStack** (not `NavigationSplitView`) so the layout
matches the WebUI's flat chrome:

- **Sidebar** (fixed `Niq.sidebarWidth` = 286pt): brand band with gateway status
  and refresh; **项目视图** (对话 / 事件 / 审批); **项目资源** (工作单元 /
  MCP 工作单元 / 程序); **工作单元选择** (search + 展开 + 查看全部, then a
  bounded checklist that filters the conversation's events; each row has a
  context menu: 提及 / 查看详情 / 只看此单元 / 休眠 · 唤醒); **对话视图设置**
  (流式模式 / 展开思考 / 折叠过程事件 / 仅回复); **资源** (项目 / 模板 /
  模型供应商); **当前项目** (switcher + start/stop/restart); a footer with the
  project, the 外观 (system/light/dark) picker and gateway state.
- **Detail** switches on `AppModel.section`; every screen owns a `PageHeader`
  (or the talk view's own header) at `Niq.headerHeight` so the two columns'
  hairlines line up.

`ContentView` applies the 外观 preference (`.preferredColorScheme`), presents
`WorkerPickerSheet` (展开) and `WorkerDetailView` (via `AppModel.workerDetail`)
as sheets, and hosts the transient error toast.

⌘1–⌘9 jump to 对话 / 事件 / 审批 / 工作单元 / MCP / 程序 / 项目 / 模板 /
模型供应商 (declared in `App`).

The shell owns **layout and navigation only**; each screen lives under
`Features/` and reads `AppModel` from the environment.
