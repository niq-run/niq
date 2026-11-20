# niq 目录划分

> 状态：as-built（2026-11-20）。这份文档只描述**代码目录的归属规则**；`~/.niq` 的运行时数据布局见 `design/project/README.zh-CN.md`。
> 目标形态（launcher / shell / project api 的分层）见 [application-architecture.zh-CN.md](./application-architecture.zh-CN.md)；本文会在其落地后同步。

## 0. 一句话

`core/` 放**契约**和**与部署形态无关的机制**；`app/` 放**部署形态**、**共享运行时**和**产品入口**。依赖只能从 `app` 指向 `core`，形态之间平级。

## 1. 源码树

```
niq/
├── core/
│   ├── itfs/                     # 契约层：只有接口与值类型，没有实现、没有 IO
│   │   ├── worker/               #   Lifecycle / Manifest / Backend / WorkerInfo / Placement
│   │   ├── bus/                  #   Identity / Control / Store / Channel
│   │   ├── event/                #   事件与匹配模式
│   │   ├── program/              #   program 的读写契约
│   │   └── llm/                  #   模型调用契约
│   └── impl/                     # 机制层：契约的实现 + worker 类型库
│       ├── workerlife/           #   活实例机制：build/spawn/snapshot/suspend/resume/restart/destroy
│       ├── busctl/               #   总线身份控制面（Store 注入，Store 适配在形态里）
│       ├── eventbus/             #   engine、registry（运行时身份投影）、HTTP transport
│       ├── workers/<type>/       #   reason / coding / workspace / mcp / timer / host / …
│       ├── iface/                #   interface worker 基类
│       ├── baseworker/           #   工具/presence 的公共实现
│       ├── reason/               #   推理引擎（transcript、provider 适配）
│       ├── provider/             #   LLM 供应商实现
│       └── embedws/              #   workspace 的文件系统后端
│
├── app/
│   ├── standalone/               # 部署形态：本地单机 niq
│   │   ├── home/                 #   host 根解析：NIQ_HOME / ~/.niq / auth 路径
│   │   ├── control/              #   多 project 编排、聚合 UI、代理
│   │   ├── project/              #   单 project 装配根
│   │   │   ├── run.go            #     启动编排（持有信号 ctx）
│   │   │   ├── project.go        #     项目路径 + 创建/列出/链接（IO 在 store）
│   │   │   ├── store/            #     全部存储实现（路径驱动，不知道 project id）
│   │   │   │   ├── project.go    #       project.json
│   │   │   │   ├── manifest.go   #       workers.json + worker.ManifestStore
│   │   │   │   ├── state.go      #       state.json + workerlife.WorkerStore
│   │   │   │   ├── identity/     #       identities.json 的 bus.Store（运行时 + 迁移共用）
│   │   │   │   └── event/        #       事件存储（SQLite 实现）
│   │   │   ├── migrate/          #     旧格式 → 当前 schema（唯一读旧格式的地方）
│   │   │   ├── template/         #     模板与 preset
│   │   │   └── provider/         #     provider.json 读取与启动门控
│   │   ├── worker/               #   worker 控制面（自闭环）
│   │   │   ├── controlplane.go   #     New / Recover / Stop / HostBackend / Manifest
│   │   │   ├── backend.go        #     worker.Backend：placement 路由 + 生命周期
│   │   │   ├── builders.go       #     每类型一个构造器
│   │   │   ├── registry.go       #     type → builder + 参数契约
│   │   │   ├── declaration.go    #     Manifest 模型
│   │   │   ├── identity.go       #     首次连接的身份播种
│   │   │   ├── store.go          #     ManifestStore 契约
│   │   │   └── process/          #     OS 子进程：provision + supervisor
│   │   └── menubar/              #   macOS 菜单栏壳（Swift，驱动 CLI，不在 Go import 图）
│   │
│   ├── cluster/                  # 部署形态：分布式（占位，与 standalone 平级）
│   │   └── README.md
│   │
│   ├── webui/                    # 共享运行时：HTTP/SSE + SPA 服务
│   │   └── assets/               #   前端源码与构建产物（dist 是 go:embed 的）
│   │
│   └── publish/                  # 产品入口 + 发行物
│       ├── cmd/                  #   niq CLI（组合根：装配 standalone）
│       ├── npm/                  #   npm 包与发布脚本
│       └── release-notes/
│
├── sdk/ts/                       # 进程外 TypeScript SDK（HTTP 客户端 + InterfaceWorker）
├── doc/                          # 仓库内文档（本文件）
└── Makefile / go.mod / README.md
```

构建产物 `bin/`、`build/`、`dist/` 不入库，也不属于目录划分。

## 2. 四类东西，四种归属

| 类别 | 判据 | 放哪 |
|---|---|---|
| **契约** | 只有接口/值类型；被多方实现 | `core/itfs/*` |
| **机制** | 与部署形态无关的实现；不读项目文件 | `core/impl/*`（`workerlife`、`busctl`、`eventbus`、worker 类型库） |
| **部署形态** | 知道 placement、项目文件布局、进程模型、存储适配 | `app/<form>/`（`standalone`、`cluster`） |
| **共享运行时 / 产品入口** | 跨形态复用（`webui`），或负责把某个形态装起来（`publish`） | `app/` 顶层 |

`app/standalone/home` 是形态内的“host 环境”知识（`NIQ_HOME`、`~/.niq`、auth 路径），不是共享运行时：`app/webui` 不能 import 它，auth 文件路径由调用方传入。

## 3. 为什么 `busctl` 和 `workerlife` 都在 core

它们都是**与形态无关**的：

- `busctl.ControlPlane` 只认 `bus.Identity` + 注入的 `bus.Store`，不碰文件系统（`go list` 的 import 只有 `fmt/sort/sync` + 契约）；standalone 注入 `project/store/identity`，cluster 换成 KV 即可复用。
- `workerlife.Manager` 只认 `WorkerConfig` / `SpawnSpec` / `ManagedWorker`，不管 placement、总线语义、类型注册。

worker 的**控制面**不在这里，因为 placement 集合、builder 表、进程托管是形态知识，所以它在 `app/standalone/worker`。cluster 会带自己的控制面，但可以复用这两个库。

## 4. 依赖方向（硬规则）

```
app/publish ──▶ app/standalone ──▶ app/webui ──▶ core
                                  └────────────▶ core
app/cluster ──▶ core
```

- `app/standalone/*` 与 `app/cluster/*` **互不 import**；
- `app/webui` **不 import 任何形态包**（它只拿 `worker.Manifest`、`bus.Control`、`IdentityRegistry`、`EventLog` 这些 core 句柄）；
- 形态包可以 import `app/webui`（装配自己的界面）；
- `app/publish` 是组合根，可以依赖形态；**形态不反向 import 它**。

可执行校验：

```sh
go list -deps ./app/webui | grep -E 'app/(standalone|cluster)'        # 应为空
go list -deps ./app/standalone/... | grep 'app/cluster'               # 应为空
go list -deps ./app/cluster/...    | grep 'app/standalone'            # 应为空
```

## 5. 新增东西放哪里

| 想做的事 | 放哪 |
|---|---|
| 新增 worker 类型 | `app/standalone/worker`：`params.go` 参数契约 + `builders.go` 构造器 + `registry.go` 注册 |
| 新增 placement | `core/itfs/worker` 常量 + `app/standalone/worker/backend.go` 路由 |
| 新增总线身份规则 | `core/itfs/bus` 契约 + `core/impl/busctl` |
| 新增实例生命周期能力 | `core/impl/workerlife` |
| 新增部署形态 | `app/<form>/`，实现 `worker.Backend` 与自己的一套存储/placement |
| 新的形态内存储 | 该形态的 `store/` 子包（例如 `project/store/identity`） |
| 新的上层界面 | 实现 `iface.Backend`，由形态装配一个 interface worker（不需要 manifest） |
| 新的 CLI 命令 / 发行配置 | `app/publish` |
| 跨形态共享的 UI / HTTP | `app/webui`（或 `app/` 下新的共享包，且不得 import 形态） |

## 6. 容易放错的三个位置

1. **把 `webui` 需要的路径/布局直接 import 进 webui**：应该由调用方传入（auth 路径就是这样解耦的）。
2. **把某个形态的文件格式放进 core**：core 只认 `Store`/`ManifestStore`/`WorkerStore` 接口，JSON 文件属于形态。
3. **把 CLI 放进形态目录**：`publish` 是产品二进制与发行物，未来 cluster 命令也要挂在同一个二进制上。
