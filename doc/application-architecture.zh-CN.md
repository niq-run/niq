# niq 应用架构（standalone 形态）

> 状态：**target（2026-11-20）**。本文描述优化后的目标形态，不是当前代码的 as-built 快照。
> 当前目录见 [directory-layout.zh-CN.md](./directory-layout.zh-CN.md)；本文与它的差异集中在 §9 映射表。
> 本文与实现同步推进：每落地一步，更新 §9 的状态列。

---

## 1. 一句话

niq 是**一个总线内核 + 两种部署形态**。standalone 形态由四部分组成：

```
一个 launcher（领域） + 两个 shell（cli / webui） + 每项目一个 runtime 进程 + 一套共享 store
```

三个概念必须分清，后面所有结论都由它们推出：

| 概念 | 回答的问题 | 判断标准 |
|---|---|---|
| **领域（domain）** | 系统知道什么、能做什么 | 与"谁来触发"无关；launcher 与 project 是两个领域 |
| **形态（form）** | 这些能力由谁提供、怎么落地 | standalone 用本机进程；cluster 用调度器与远程节点 |
| **外壳（shell）** | 人怎么够到这些能力 | 只做入口编排与传输，不实现能力 |

---

## 2. 进程模型

standalone 有**三种进程**，不是"一个主进程 + 子模块"：

| 进程 | 生命周期 | 职责 | 监听 |
|---|---|---|---|
| **shell 进程** | 常驻（`niq` 拉起） | 托管 SPA、auth、`/p/{id}` 代理、launcher 的 HTTP 面孔 | 唯一可对外绑定的端口（默认 `127.0.0.1:9527`） |
| **project 进程** | 每个项目一个，随项目起停 | 总线、worker 生命周期、人类入口（interface worker）、项目 JSON API | 仅 loopback，端口来自 `project.json` |
| **CLI 调用** | 短命 | launcher 的另一种面孔；`project run` 例外，它会**变成** project 进程 | 不监听 |

关键事实：

- **一个二进制，三种身份**。`niq`、`niq shell`、`niq project run <id>` 是同一个可执行文件。shell 用 `os.Executable()` + `project run` 拉起项目，和 `docker` 拉起容器进程同构。
- **launcher 不是守护进程**。项目实例由端口识别，不由父子关系识别；`niq project stop` 在 shell 没跑时同样有效。shell 重启不影响任何运行中的项目。
- **人类身份在项目里**。interface worker（`webui` 这个总线身份）运行在 project 进程内，它的 backend 是项目 API。浏览器和未来的 TUI 都是这个 API 的客户端，**不持有总线凭证**（见 §6）。

```
                      ┌──────────── shell 进程 ────────────┐
  浏览器 ── HTTP ────► │ shell/webui                        │
  （远程需 auth）      │   SPA · auth · /p/{id}/* 代理       │
                      │        │                  │        │
                      │        └─ launcher（同进程）│        │
                      └────────┼──────────────────┼────────┘
                               │                  │ HTTP（loopback）
  终端 ── exec ──►  shell/cli  │                  ▼
                      └─► launcher（同进程）   ┌──────── project 进程 ────────┐
                               │               │ api（JSON + SSE + 人类入口） │
                               └── exec ──────►│ 总线 · worker 控制面          │
                                               └──────────────┬───────────────┘
                                                              ▼
                                                           store
```

---

## 3. 包与职责

```
app/
├── standalone/
│   ├── shell/
│   │   ├── webui/        HTTP 外壳：SPA 托管、auth、/p/{id} 代理、launcher 路由
│   │   ├── cli/          `niq` 命令树：launcher verbs、shell 守护、picker；`project run` 交接
│   │   └── menubar/      macOS 菜单栏壳（Swift，驱动 CLI，不持有状态）
│   ├── launcher/         领域：项目目录、模板、provider、端口、日志、起停/链接
│   ├── project/          运行时：总线和 worker 的装配与恢复
│   │   └── api/          项目 HTTP API：项目数据查询/操作、SSE、人类入口（iface.Backend）
│   ├── store/            落盘、路径与本地后端的唯一主人
│   │   ├── root.go         NIQ_HOME / 项目目录 / 文件名 / auth 与 provider 路径
│   │   ├── project.go      project.json
│   │   ├── manifest.go     workers.json / workers.archived.json
│   │   ├── state.go        workers/<id>/state.json
│   │   ├── program/        <proj>/programs 的 program.Store 适配（薄封装 pgbackend）
│   │   ├── migrate/        旧格式 → 当前格式
│   │   ├── identity/       identities.json（bus.Store 实现）
│   │   └── event/          事件存储（SQLite）
│   ├── worker/           worker 控制面：placement、builder 表、恢复与收尾
│   │   └── process/        OS 子进程监督（机制）
│   └── provider/         LLM provider 配置（launcher / api 共享；worker 不再直接依赖）
├── cluster/              另一形态（占位；自带 UI，不复用 shell/webui）
├── webui/                前端源码（组件 + 构建）；两个形态的 UI 各自 embed 产物
└── publish/              组合根 + 发行物：薄 main、npm、install.sh、goreleaser
```

### 职责表

| 包 | 负责 | **不负责**（硬性） |
|---|---|---|
| `shell/webui` | HTTP 入口、SPA、auth、`/p/{id}` 代理、把 launcher 请求转成 HTTP | 不 import `project`；不碰总线；不持有项目状态 |
| `shell/cli` | 命令解析、flag、shell 守护（pid/日志）、picker、把 `project run` 交给 runtime | 不实现起停能力（调 launcher）；不做 HTTP 代理 |
| `shell/menubar` | 菜单栏交互 | 不持有状态：每次 `niq status --json`，每个动作一次 `niq` 调用 |
| `launcher` | 项目目录视图、create/link/start/stop/restart、模板、provider、端口分配、日志定位 | 不监听端口；不渲染；不 import `shell/*` |
| `project` | 单项目 runtime：装配总线、恢复 worker、收尾、快照 | 不认识其他项目；不 import `launcher` / `shell/*` |
| `project/api` | 项目数据的 JSON/SSE 契约、人类入口、审批 inbox | 不托管 SPA；不做 auth；不认识 launcher |
| `store` | 根与路径解析、每种落盘形状的读写、原子写、迁移入口 | 不 import `project` / `launcher` / `shell/*` |
| `store/program` | program 文件的路径约定与 `program.Store` 适配 | `pgbackend` 仍是 core 机制（core 测试要用），这里只封装路径 |
| `worker` | placement → 机制、类型注册表、恢复顺序、保护集 | **只消费注入的 handles**：不 import `store` / `provider`，不读 env、不拼路径 |
| `provider` | provider 配置读取与启动门控 | 不认识项目实例；路径来自 `store` |

---

## 4. 依赖规则

```
store            → store/migrate, store/program, worker（仅 Manifest 等类型）
provider         → store（取配置路径）
launcher         → store, provider
project          → store, worker, provider
project/api      → store, worker 契约, core（不 import project 内部）
worker           → core + 注入（program.Store / provider handles）
shell/cli        → launcher, project（仅 RunProject 交接）
shell/webui      → launcher, store（读端口）, 前端产物
```

读法：`worker` 不再 import `store` / `provider`，所以 `provider → store → worker` 不会成环。`launcher` 不 import `project`：它启动实例靠 exec 自己，不靠包调用。

硬规则，可用命令校验：

1. **`shell/webui` 不 import `project`**。它只知道项目 id 与端口（经 `store`/launcher），项目数据一律走代理。
   ```sh
   go list -deps ./app/standalone/shell/webui | grep 'standalone/project$'   # 必须为空
   ```
2. **`shell/*` 不实现领域能力**：项目的起停、端口、模板只在 `launcher`。（`shell/cli` 允许 exec 自己，用于把 shell 进程放到后台——那是外壳自身的生命周期。）
3. **`launcher` 不 import `project`**：启动实例靠 exec `niq project run`，是进程边界，不是包依赖。
4. **`project` 不 import `launcher` / `shell/*`**。
5. **`store` 不 import `project` / `launcher` / `shell/*`**。
6. **`core` 不 import `app`**；`app/standalone` 与 `app/cluster` 互不 import。
7. **数据形状只有一处**：任何 JSON 文件的 marshal/unmarshal 只在 `store`（前端契约除外）。
8. **路径只有一处**：根、项目目录、文件名、auth 与 provider 路径都在 `store`；`worker` 不读 env、不拼路径，所需的 store 与 provider 能力由 `BuildContext` 注入。
   ```sh
   grep -rn 'standalone/home\|project/provider\|standalone/store' app/standalone/worker  # 必须为空
   ```

### provider 与 store 的配合（依赖倒置）

`provider.json` 的 **IO 与路径**归 `store`，**语义**归 `provider`：

```
store/provider.go    ProviderPath / LoadProvider / SaveProvider / EnsureProviderExample
                     NIQ_PROVIDER_CONFIG 覆盖与 <root>/common/providers/provider.json 都在这里
        │  *provider.Config（值）
        ▼
provider（纯语义，不 import store）
        Find(cfg, name) / Default(cfg) / Build(entry) / NewProviderSources(cfg, ...)
        Loader func() (*Config, error)   ← 函数类型定义在 provider，由装配层用 store 实现
        ▼  由装配层注入 BuildContext
worker   ctx.Providers.Find / ctx.ProviderSources
```

统一规则：**`store` import 领域值类型（`Manifest`、`provider.Config`）做序列化；领域包不 import `store`**，只接收 store 读出来的值。这样 `store → worker(Manifest)` 与 `worker → provider` 不会成环，`home` 也就能真正并入 `store`。

注意这不是新增一层"providerStore"：它只是 `store` 里的**又一个文件适配器**，与 `project.go` / `manifest.go` / `state.go` / `identity/` / `event/` / `program/` 并列。`provider` 包本身不消失，只是从"既管文件又管语义"收缩为"只管语义"。

**定位相同，形态不必强求一致**：`store` 里本来就有三种形态——根包函数族（`project.go`）、实现 core 契约的带锁类型（`Manifest` / `State` / `identity.Store`）、以及工厂返回接口（`eventstore.New`）。provider 没有 core 契约、`provider.json` 又是宿主级单文件，所以取最接近的 `project.go` 那种根包函数族即可；不要为了对称硬造一个没有契约的 `Store` 类型。将来若真要在运行时缓存/反复读写，再升级为带类型适配器。

调用方的变化：

- shell 的 `/api/providers`：`store.LoadProvider` 读、`store.SaveProvider` 写，`provider` 只做校验与 Build。
- 启动门控：`provider.EnsureLLMConfigured(cfg, workers)`，`cfg` 由 store 读出。
- worker builder：删掉 `providerpkg.Find` / `NewProviderSources` 的全局读取，改用注入的 handles；运行时切换 provider 时，通过注入的 `Loader` 重新读盘（今天的 lazy `Load()` 行为不丢）。

---

## 5. 请求路径（含未来 TUI）

| # | 入口 | 目标 | 传输 | 状态在 |
|---|---|---|---|---|
| 1 | `niq project list/create/start/stop` | launcher | 同进程调用 | `store` |
| 2 | `niq project run <id>` | project runtime | **就是该进程** | `store` + bus |
| 3 | `niq` / `niq shell` → 浏览器 `/`、`/api/projects`、`/api/templates`、`/api/providers` | shell + launcher | HTTP | shell 进程 |
| 4 | 浏览器 `/p/{id}/api/*` | project api | HTTP 代理（loopback） | project 进程 |
| 5 | 未来 `niq tui` | project api | HTTP + SSE | project 进程 |

第 3 条里的 shell 进程**不代理自己**：launcher 的接口由 shell 直接实现（它在同一个进程里），只有项目数据才经过代理。这是"一个 origin、两个 server、一个代理"的全部内容。

---

## 6. auth 与身份

### auth 属于 shell，不属于项目

| 规则 | 内容 |
|---|---|
| 归属 | `shell/webui` 独占：凭证文件、启动协商、BasicAuth 包装 |
| 凭证 | `<niq root>/common/auth.json`，跨项目共享，不属于任何项目目录 |
| 执行点 | 每个**可被远程访问**的 listener。今天只有 shell 的 listener 满足 |
| 免认证 | 绑定 loopback 时不需要，也不告警 |
| 强制 | 非 loopback 绑定必须有凭证，否则启动告警（不静默放行） |

**推论（行为变化）**：project API **只绑 loopback**。`--webui 0.0.0.0:x` + `--webui-auth` 这条"直连单项目"的路径取消，远程访问统一走 shell。这样只有一个对外入口、一个 auth 执行点，不会出现"两道门、一份凭证"。

### 总线身份属于项目

`identities.json` 跟着项目走，由项目进程的 bus 控制面管理。UI 外壳**不使用**总线身份：

- 发消息 → `POST /api/input`（项目以人类身份发布）
- 审批 → `POST /api/approvals/{id}/decision`
- 驱动 worker → `POST /api/workers/{id}/event`

因此 **TUI 不需要总线凭证**，只需要项目 API 的地址（远程时加 auth）。iface worker 仍是项目的，审批 inbox 也在项目里，浏览器关掉不会丢。

> 若将来需要"多人、多身份同时在线"，那才需要让 UI 自己成为总线参与者（每个会话一个身份、一份凭证）。那是独立特性，不是基线。

---

## 7. 磁盘布局与所有权

| 路径 | 主人 | 读者 |
|---|---|---|
| `<root>/shell.json` | `shell`（cli 启动时写，进程内更新） | `niq status`、menubar |
| `<root>/common/auth.json` | `shell/webui` | 仅 shell |
| `<root>/common/providers/provider.json` | `provider` | launcher、api、worker |
| `<root>/projects/<id>/project.json` | `store` | launcher、shell、project |
| `<root>/projects/<id>/workers.json` | `store` | launcher、project |
| `<root>/projects/<id>/workers.archived.json` | `store` | 同上 |
| `<root>/projects/<id>/identities.json` | `store/identity` | project（bus 控制面） |
| `<root>/projects/<id>/root_secret` | `store` | project（token signer） |
| `<root>/projects/<id>/workers/<id>/state.json` | `store` | project |
| `<root>/projects/<id>/logs/niq-<date>.log` | project runtime | shell / launcher（定位与展示） |

规则：**谁写谁拥有**。shell 不写项目文件；launcher 只通过 `store` 写；project 只写自己的 state、日志和 `identities.json`。
所有路径由 `store` 计算（`AuthConfigPath`、`ProviderConfigPath`、`ProjectDir` 等），本表的"主人"指的是**内容的所有权**，不是路径的拼装权。

---

## 8. 关键不变量

1. **形状与路径都只有一个主人**：都在 `store`。别的包只调 API 或用注入的 handle，不拼 JSON、不拼路径。
2. **外壳不持有领域状态**。杀掉 shell，所有项目照常跑；重启 shell，状态从磁盘重建。
3. **项目不依赖 shell 存活**。实例由端口识别，起停靠信号 + 端口探测。
4. **对外入口唯一**。只有 shell 可以绑非 loopback；项目 API 永远 loopback。
5. **人类身份属于项目**。iface worker、审批 inbox 都在 project 进程。
6. **SPA 只由 shell 托管**。project 只出 JSON/SSE，不出 HTML、不 embed 前端。
7. **形态互不 import**；`core` 不知道 `app` 的存在。
8. **UI 客户端不需要总线凭证**。凭证只属于项目进程。

---

## 9. 与当前代码的映射

| 现在 | 目标 | 动作 | 状态 |
|---|---|---|---|
| `app/standalone/control`（领域 + HTTP 混体） | `launcher`（领域）+ `shell/webui`（HTTP） | 拆 | 未开始 |
| `app/publish/cmd`（命令树 + 守护 + picker） | `shell/cli` + `publish/cmd`（薄 main） | 移 | 未开始 |
| `app/standalone/menubar` | `shell/menubar` | 移 | 未开始 |
| `app/webui`（Go 包：SPA + 项目 API + iface.Backend + auth） | `project/api` + `shell/webui` + `app/webui`（仅前端） | 三分 | 未开始 |
| `app/standalone/home` | `app/standalone/store`（root.go） | 并入，不再有独立包 | 未开始 |
| `project.go` 的 `ProjectsRoot/ProjectDir/ProjectPath/LogPath/RootSecretPath` | `store`（root.go） | 移 | 未开始 |
| `app/standalone/project/store` | `app/standalone/store` | 移 | 未开始 |
| `app/standalone/project/migrate` | `app/standalone/store/migrate` | 移 | 未开始 |
| `app/standalone/project/provider` | `app/standalone/provider` | 移（去掉 `worker → project/*` 反向边） | 未开始 |
| `project.go` 的 catalog/create/link | `launcher` | 移 | 未开始 |
| `~/.niq/control.json` | `~/.niq/shell.json` | 改名 | 未开始 |
| `niq control [-f]` | `niq shell [-f]`（裸 `niq` 同义） | 改名 | 未开始 |

已知偏差（落地时要一并处理）：

- **为什么不能直接把 `home` 并进 `store`**：`worker` 里有两条隐藏的全局路径读取——`buildProgramSpec` 的 `home.Root()/programs` 回退，以及 `resolveReasonFamily → providerpkg.Find → provider.Path() → home.Root()`。只要它们还在，`home` 一并入 `store` 就成环：`store → worker → provider → store`。**两条都要切断**：program store 与 provider 能力都从 `BuildContext` 注入；只搬 program store 不够（provider 那条边还在）。
- **`pgbackend` 留在 core**：core 的 program worker 测试直接 import 它，core 不能反向依赖 app。搬到 app 的只是路径约定与适配层（`store/program`）。
- `store/manifest.go` 目前 import `project/migrate`，而 `migrate` 又 import `worker`（→ `provider`）。**store 不能依赖 worker 类型库**：`store/migrate` 应只做 JSON 形状转换，输出 `map[string]any`，由 `store` 反序列化；或接受这一条编译期依赖并在文档中注明。推荐前者。
- `webui.Server` 一物四职（项目 API / SPA / iface.Backend / auth），必须先拆 API 再拆 SPA，否则 TUI 会被 SPA 绑架。

---

## 10. 落地顺序

每一步独立可交付、可回滚，顺序不可颠倒。

### 第 1 步：store 抽离

- `app/standalone/home` → `app/standalone/store`（root.go）；`project` 的根与目录/文件名函数一同迁入
- `store/program` 收下 `<proj>/programs` 路径约定，产出 `program.Store`，由 `BuildContext` 注入；`buildProgramSpec` 删除 `home.Root()` 回退
- `BuildContext` 注入 provider 校验与 `reason.ProviderSources`；worker 删除 `providerpkg.Find` / `NewProviderSources` / `InitialProviderInfo` 的全局读取
- `app/standalone/project/store` → `app/standalone/store`
- `project/migrate` → `store/migrate`
- `project/provider` → `app/standalone/provider`
- `project` 保留 `RunProject`，其余迁走

**验收**：
```sh
go list -deps ./app/standalone/store | grep 'standalone/project'   # 空
grep -rn 'standalone/home\|project/provider\|standalone/store' app/standalone/worker  # 空
grep -rn 'standalone/home' --include=*.go .                        # 空
```
全部测试通过；无行为变化。

### 第 2 步：launcher 抽离

- 从 `control/projects.go` 与 `cmd/pick.go` 收拢：list/create/link/start/stop/restart、端口分配、日志定位、模板、provider
- 删除两份重复实现（`launchProject` 与 `startProjectDetached`）

**验收**：`launcher` 之外不再出现 `os.Executable()` + `project run` 的拼装；CLI 与 WebUI 的起停行为一致（同一份等待与错误文案）。

### 第 3 步：project/api 拆分

- 项目数据路由 + SSE + `iface.Backend` → `project/api`
- `shell/webui` 只保留 SPA、auth、代理、launcher 路由
- 项目进程取消静态资源与 HTML fallback

**验收**：直连项目端口只返回 JSON/SSE；`/p/{id}/` 在项目停机时仍能渲染（由 shell 托管 SPA）；TUI 可用 `curl` 走通 talk/审批。

### 第 4 步：shell 归位与重命名

- `control` → `shell/webui`；`publish/cmd` → `shell/cli` + 薄 main；`menubar` → `shell/menubar`
- `niq control` → `niq shell`；`control.json` → `shell.json`

**验收**：`niq`、`niq shell`、`niq status`、`niq project *` 行为不变（除命名）；`go list` 的依赖规则全部成立。

---

## 11. cluster 的复用边界

| 复用 | 说明 |
|---|---|
| `core/*` | 完全复用：总线、worker 契约、机制库 |
| `store` 契约 | `bus.Store`、`worker.ManifestStore` 由 cluster 换 KV 实现；`store` 本身是 standalone 的文件适配，不直接复用 |
| 前端组件 | `app/webui` 的组件可复用；构建产物与路由属于各自的 shell |

| 不复用 | 原因 |
|---|---|
| `launcher` | 调度、节点选择、健康检查是另一套领域 |
| `shell/webui` | cluster 的 UI 单独实现，不绑定 standalone 的壳 |
| `project` 的进程形态 | 单机 runtime 假设（本机总线、本机文件）；cluster 的执行体另行设计 |

一句话：**core 共享，store 只共享契约，UI 只共享组件，launcher 与 shell 各自实现。**

---

## 12. 命名对照

| 旧 | 新 | 说明 |
|---|---|---|
| control plane | **shell** | 人访问 niq 的入口；不再是"控制面"（那个词留给 `worker.ControlPlane`） |
| control | **launcher** | 项目启动器 + 共享资源管理器（模板/provider/端口） |
| WebUI（项目内） | **project api** | 项目数据的 HTTP 契约，TUI 也消费 |
| CLI | **shell/cli** | 与 `shell/webui` 并列的另一套壳 |
| home | **store**（root.go） | 根与路径解析并入 store |
| declaration | **manifest** | 已落地 |
| worker service | **workerlife.Manager** | 已落地 |

`shell` 一词只指**外壳**，不指 `sh`、不指"命令解释器"。`niq shell` 与裸 `niq` 同义：确保 shell 进程在跑。
