# niq 架构

> 面向扩展者。这份文档解释**为什么这样分层**，以及加东西时该放在哪。
> 具体目录与包清单见 [directory-layout.zh-CN.md](./directory-layout.zh-CN.md)；
> 落地步骤与阶段状态在设计文档仓库的 `design/app/`。
>
> 本文描述**目标架构**。与今天代码的差距集中列在 §10；行内用 ⏳ 标注尚未落地的部分。

## 0. 一句话

niq 是**一个二进制、多个进程身份、若干分发形态**。

读懂它只需要分清四件常被混为一谈的事：装配、入口、宿主、分发。

## 1. 四个概念

| 概念 | 是什么 | 谁来做 |
|---|---|---|
| **装配根**（composition root） | 一段代码：构造 store 路径、launcher、gateway，决定挂载哪些 UI | 与被装配的长驻进程**同进程** |
| **入口**（entry） | 用户启动的那个程序 | `publish/cmd` 的 main（未来是 `publish/mac` 的 .app） |
| **宿主**（host） | 长期存活、拥有 gateway 与项目子进程的进程 | gateway 守护进程 |
| **分发**（distribution） | 打包产物：CLI 二进制、.app、npm 包、zip | `app/publish/*` |

两条不变量，后面所有结论都由它们推出：

1. **装配永远与 gateway 同进程。** 装配根不是"最终用户入口"，它是一段和守护进程一起运行的代码。
2. **UI 永远是客户端。** UI 进程里没有任何装配代码，它只通过 HTTP 看 niq。

这条比"UI 属于哪个形态"重要得多：menubar、desktop、浏览器、终端，在这里是同一类东西。

## 2. 进程模型

```
$ niq w                    （浏览器形态）
终端 ──► [cli 前端] ──确保/exec──► [gateway 守护] ──HTTP──► 浏览器
                                        │ exec(self, __project-run, id)
                                        ▼
                                   [project 子进程] × N

$ niq i                    （终端形态）
终端 ──► [cli 前端] ──确保──────► [gateway 守护] ◄──HTTP── [tui 进程]
```

四个要点：

- **project 必须是独立进程**：一个项目崩了不影响别的项目，可以单独重启，有自己的端口、日志与信号处理。
- **一个二进制靠 argv 切换身份**：`main` 只做一次二选一分派——私有 verb → `launcher.ChildMain`；其余 → `cli`。`cli` 再分第二次（webui / tui / 守护生命周期）。
- **私有 verb 只有一个主人**：字符串是 launcher 的常量，由 `launcher.IsChild` 判断。cli 不认识它，也不会复制它的值。
- **项目由端口识别，不由父子关系识别**：所以"停"对 shell 亲手拉起的实例和别处拉起的实例是同一条路径。

⏳ 图中 `niq i` 的"确保 gateway"是目标：今天 tui 与 launcher 同进程，尚未经过 gateway。

## 3. 装配根：每个长驻进程一个

| 进程 | 装配根 | 装配什么 |
|---|---|---|
| gateway 守护（长驻） | `standalone` 的启动路径（由 `cli` 触发） | store 路径、launcher、gateway（auth 配置、UI 挂载表） |
| project 子进程（每项目一个） | `launcher.ChildMain` → `project.RunProject` | store、provider、event store、bus/ctrl、worker 注册表与恢复 |
| CLI 前端（短命） | `cli` 的路由 | 几乎不装配：读路径，决定拉起谁 |

由此回答一个常见误解：**`project` / `busctl` / `worker` 不属于前端装配根**。它们只在 project 子进程里装配。如果 `cli` 去装配 worker，`cli` 就必须在项目进程里——那正是被删掉的 `--foreground` 路径。

`provider` 是无状态函数集合（`Find` / `Build` / `ProviderSources`），两侧都用但不需要"装配"；被装配的是 `store`（谁读 `provider.json`）。

## 4. 领域分层

每个包只回答一个问题：

| 包 | 只回答 | 不做什么 |
|---|---|---|
| `store` | 东西存在哪、长什么样 | 不认识 project / launcher / shell；不解析语义 |
| `provider` | provider 怎么解析、怎么变成 `llm.LLMProvider` | 不碰文件；没有实例、没有生命周期 |
| `launcher` | 项目怎么起、停、迁 | 不渲染；不监听端口；不 import shell / cli |
| `project` | 一个项目内部怎么跑 | 不认识其他项目；不认识 shell |
| `project/api` | 项目数据的 HTTP 契约（JSON + SSE） | 不托管 SPA；不做 auth |
| `worker` | 实例怎么建、怎么恢复 | 不读全局配置、不拼路径；依赖全部注入（`env:` / `file:` 是声明里的凭证引用，不是配置来源） |
| `gateway` | 人访问 niq 的唯一 HTTP 端点 | 不认识具体 UI；不在自己进程里跑项目 |
| `ui/*` | 人看到什么、怎么操作 | 不含装配；不 import 领域 |

### 依赖（编译期）

```
main ──► cli ──► gateway ──► launcher ──► project ──► worker ──► core
          │         │  └──► store             └──► provider
          │         └──► contract（wire 类型）
          ├──► ui/webui（只取 FS）
          └──► ui/tui ──► contract（client，仅 stdlib）
```

### 依赖（运行期）

```
前端进程：main → cli → gateway → launcher.Start(id)
                                      │ exec(self, ChildVerb, id)
                                      ▼
子进程：          main → launcher.ChildMain → project.RunProject
```

**这两张图不能混着画。** 编译期的 `launcher → project` 只服务 `ChildMain`；`Start` 与 `ChildMain` 同包，但从不在同一进程。所以既没有包环，也没有"shell 在自己进程里跑项目"。

## 5. gateway：唯一 HTTP 端点

gateway 一个进程承担四件事：

1. **auth 终止**（唯一认证点）
2. **`/api/*`**：launcher 领域的契约（项目、模板、provider）
3. **`/p/{id}/api/*`**：代理到项目进程；其余路径返回 SPA 与 index fallback
4. **UI 挂载**：把静态资产挂在指定前缀下

### 为什么所有 UI 都要走它

如果每个 UI 自己对接 launcher 和 project api，成本是 **N 个 UI × M 个领域**；而且每个 UI 都要自己实现 auth、端口发现、代理规则。收敛之后是 **N 个客户端 × 1 个网关**。

所以 tui 也走 HTTP，即使它和 gateway 在同一台机器上。代价是一个守护进程的依赖，换来的是：**加一个领域（文件、权限、远程资源）时，所有 UI 自动获得，零改动。**

### UI 挂载是扩展点

```go
gateway.Options{
    UI: []gateway.UIMount{
        {Prefix: "/",       FS: webui.AssetsFS()},
        {Prefix: "/files/", FS: filebrowser.AssetsFS()},   // 未来
    },
}
```

gateway **不认识 webui**，只认 `UIMount`。填这张表的是分发层（组合根）。

这带来两个直接能力：一个 standalone 可以同时提供多个 UI 面；第三方可以往 gateway 挂自己的管理界面，而不必改 core。

### 项目端口是数据端口

项目 API 只出 JSON/SSE，只监听 loopback。SPA、auth、静态回退全部在 gateway。

## 6. UI 是客户端

| UI | 随主二进制分发 | 可连远程 | 介质 |
|---|---|---|---|
| `ui/webui` | 是（embed，由 gateway 挂载） | 是（同源或 CDN） | JS/静态资源 |
| `ui/tui` | 是（同一二进制） | 是 | 终端 |
| `ui/menubar` | 否（`.app`） | 是 | Swift |
| `ui/desktop`（未来） | 否 | 是 | 原生壳 |

⏳ 今天 `webui`、`tui`、`menubar` 都在 `app/standalone/shell/` 下；`app/ui/` 是目标位置，迁移与整体目录调整一起做。

"自带"在这里是**分发属性**，不是架构属性：

> 自带 = 随主二进制分发、开箱即用。不代表同进程，也不代表同目录层级。

因此 **webui 与 menubar 在架构上是同一类东西**，区别只是字节放在哪：一个 embed 在二进制里，一个在 `.app` 里。webui 打成 CDN 静态资源后，与 menubar 的差别只剩打包方式。

## 7. 分发

| 产物 | 含 gateway 二进制 | UI 来源 | 装配根 |
|---|---|---|---|
| `publish/cmd`（`niq` CLI） | 是 | embed 的 `ui/webui` | 本产物的 main：只声明 UI 列表 |
| `publish/mac`（`niq.app`）⏳ | 是（打包 `niq`） | Swift 自绘（今天是菜单栏） | `niq` 里的 `standalone` 启动路径 |
| `publish/menubar`（未来拆出）⏳ | 否 | `ui/menubar` | 远端或本机另一台 gateway |

规则一句话：

> **产物里是否含 gateway 二进制，决定它是"standalone 的打包"还是"纯客户端"。**

一个已知的编译期约束：`go:embed` 在编译期决定资产是否在二进制里，挂不挂是运行期决定。Swift 包裹的 mac app 没有第二个 Go main，所以它无法在编译期裁掉 SPA；默认带上即可（1.3MB，不值得为它加开关）。

组合根只**声明**，不**构造**（目标形态；今天装配仍在 `cli`）：

```go
// publish/cmd/main.go
func main() {
    standalone.Run(standalone.Spec{
        Version: version, BuildTime: buildTime,
        UI: []gateway.UIMount{{Prefix: "/", FS: webui.AssetsFS()}},
    })
}
```

否则每个 publish 子目录都会长出一份相似的装配代码。

⏳ `publish/mac` 的 Swift 源码今天在 `app/standalone/shell/menubar`，`make miq` 产出 `dist/niq.app`；它已经是"打包了 `niq` 的 mac 分发"的雏形，只是还没有下载即全套的引导。

## 8. 扩展指南

| 想加什么 | 放哪 | 不要做什么 |
|---|---|---|
| 一个新的 UI | `app/ui/x`，用 `contract` 的客户端 | 不 import launcher/project/store；不自己实现 auth |
| 一个新的领域 | `app/standalone/x`，只向下依赖 store | 不让 shell 直接 import 它；让它经 gateway 暴露 |
| 一个新的分发 | `app/publish/x`，声明 UI 列表 | 不写装配逻辑 |
| 一个新的 worker 类型 | core 契约 + `standalone/worker` 的 registry/builder | 不改 shell；不在 builder 里读全局配置或拼路径 |
| 项目内新的 API | `project/api` | 不在 project 端口托管 SPA 或 auth |
| 一个新的远程能力 | gateway 的 `/api/*` 或挂载点 | 不让每个 UI 各自对接 |

## 9. 不变量

已经成立、可以直接 grep 校验的：

```sh
# 子进程协议只有一个主人：verb 字符串只在 launcher
grep -rn 'ChildVerb' app | grep -v 'app/standalone/launcher/'                        # 应为空

# 根与目录只有一个来源：路径知识只在 store（测试除外）
grep -rn 'NIQ_HOME' app/standalone --include=*.go | grep -v '/store/' | grep -v _test # 应为空

# worker 不 import store：它只消费注入的 handles
grep -rn 'standalone/store' app/standalone/worker --include=*.go                     # 应为空

# store 不反向依赖任何领域
go list -deps ./app/standalone/store | grep 'standalone/\(project\|launcher\)'     # 应为空

# 项目端口只出数据：不 embed 资产，也不依赖 store/worker
grep -rn 'go:embed' app/standalone/project/api                                       # 应为空
go list -deps ./app/standalone/project/api | grep 'standalone/\(store\|worker\)'  # 应为空
```

目标态、今天还不成立的：

```sh
# UI 对领域零依赖（目标：app/ui/*）
go list -deps ./app/ui/tui | grep 'standalone/\(launcher\|project\|store\)'       # 应为空

# UI 只通过 gateway 的契约看 niq
go list -deps ./app/ui/... | grep 'standalone/' | grep -v contract                   # 应为空
```

## 10. 当前与目标的差距

按影响从大到小：

1. **UI 还不在 `app/ui/` 下**：`webui`、`tui`、`menubar` 都在 `app/standalone/shell/`；tui 同进程调 `launcher`，没有 `contract` 客户端，也还不走 gateway。这是"所有 UI 走 gateway"的最后一公里。
2. **`gateway` 这个包名还没落地**：今天叫 `shell/webui`，server 与静态资产共处一包，UI 挂载表也还没抽成 `UIMount`。
3. **`launcher` 的目录能力是转发不是搬迁**：`CreateProject` / `LinkProject` 的实现仍在 `project`，`launcher` 只是唯一入口。
4. **`publish/mac` 还未成立**：Swift 源码在 `app/standalone/shell/menubar`，由 `make miq` 打成 `dist/niq.app`；迁移到 `publish/mac` 与整体目录调整一起做。
5. **`store/program` 没有抽成适配器**（为了保留 mount 覆盖能力），属于有意偏差。

这些是整理，不是设计变更：无论先做哪一条，第 1–9 节的结论都不变。
