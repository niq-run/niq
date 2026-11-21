# niq

[English](README.md) · **简体中文**

## niq 是什么
niq 是一个事件驱动的智能体运行时，它通过事件总线将一组可扩展数量和类型的工作单元串联起来，这些工作单元围绕各自的目标和关注点工作，并借助事件总线相互协作，进而构成服务用户的智能体。

## 协作即扩展
niq 的成长不是靠增加新的抽象，而是靠增加更多在事件总线上协作的参与者。每一项能力（推理、工具执行、安全守卫、生命周期）都是一个 Worker。扩展一项能力，和让一个人加入协作，是同一件事 —— 用户只是另一个协作参与者，可以有一个，也可以有很多个。在 niq 里，一切都是 Worker 在事件总线上协作。

> **状态：早期、快速演进中。** 设计仍在变化，API 与行为可能随时调整，恕不另行通知。

## 安装

共有三种安装方式，任选其一都能把 `niq` 命令放进你的 PATH。在你运行它之前什么都不会启动 —— 详见[快速开始](#快速开始)。三种方式都不需要 JS 运行时：program worker 在内嵌的 QuickJS 引擎（`modernc.org/quickjs`，纯 Go 实现）里运行脚本程序（PTC），因此不会拉起 `node` 进程，脚本也只能看到 worker 注入的 SDK —— 没有 `process`、`require`、`fetch` 或动态 `import()`。

**npm**（推荐 —— `niq update` 能让这种方式装上的版本保持最新）：

```sh
npm install -g @niq.run/niq
```

在 macOS 上，这会安装三样东西：`niq` 命令、`miq` 和 `niq.app`（复制到 `~/Applications`，因此 Spotlight 和 Finder 都能看到）。`miq` 用于打开菜单栏应用；无论是这样启动，还是双击 app，打开的都是同一个菜单。

**安装脚本**（不依赖 npm；二进制装到 `~/.niq/bin`）：

```sh
curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/app/publish/install.sh | sh
```

**从源码安装**（需要 Go 1.26+ —— 内嵌 JS 引擎要求如此 —— 以及 Node 20+ 用于打包 WebUI）。`make install` 会重新构建 WebUI，构建 `./bin/niq` 并把它软链到 `~/.local/bin`（用 `PREFIX=...` 可指定其他目录），因此只要该目录在你的 PATH 上，`niq` 在任何 shell 里都能用：

```sh
git clone https://github.com/niq-run/niq
cd niq
make install
```

如果你更希望把二进制留在 `./bin/niq` 并从检出目录直接调用，请改用 `make build`。

验证是否装好 —— 这会打印版本号并退出：

```sh
niq --version
```

**升级。** `niq update` 走的是 npm 安装包，所以它是更新 npm 安装的方式。脚本安装通过重跑那行 `curl` 更新，源码安装通过 `git pull && make install` 更新 —— 两者都是原地替换二进制。正在运行的控制面和项目仍然持有启动时的那份二进制，因此最后要用 `niq restart -p` 收尾。

## 快速开始

`niq` 已在 PATH 上之后，一条命令就能把全部启动起来。niq 分两层：**控制面（control plane）**，以及**项目（project）** —— 每个项目都是一个隔离的智能体实例，有自己的事件总线、Worker 集合，以及监听在自己端口上的 WebUI。

```sh
# 在后台启动控制面（默认 :9527）
niq
```

`niq` 会让控制面脱离终端在后台启动，打印出可访问的地址，然后回到提示符 —— 关掉 shell（或断开 SSH 会话）不再会把它停掉。再次运行 `niq` 则是对这个服务做汇报（它完全等同于 `niq status`），所以一条命令同时覆盖“启动它”和“它是否在运行？”。

```
 - niq -  version: 0.14.0 · build: 2026-11-01T23:55:12Z

[control] running  pid 4711 · since 2026-11-01 23:57
webui    http://localhost:9527
log      ~/.niq/niq.log
stop     niq stop
restart  niq restart

[projects]  niq project to start, stop or restart one
● pin   running  webui 57609  bus 57608  http://localhost:9527/p/pin/
○ notes stopped  webui 57621  bus 57620  http://localhost:9527/p/notes/
```

`niq status` 报告进程、地址和日志，以及每个项目的运行状态和 URL（端口连不上就表示该实例没在运行）；`niq stop` 会关掉它 —— 项目是各自独立的进程，所以除非你加上 `-p`，否则它们会继续提供服务；`niq restart` 会在原地址上把它重新拉起来。启动参数可以放在任何“启动”的位置（`niq --addr :8080 --auth alice:s3cret`，`niq restart` 会复用已记录的地址）。如果想让控制面附着在终端里 —— systemd、容器、`docker run`，或者只是想盯着它 —— 加上 `--foreground`（或 `-f`）：

```sh
niq control --foreground
```

打开 **http://localhost:9527** —— 控制面的 WebUI 会列出你的项目，还能从模板创建并启动一个项目，全程不需要终端。

更喜欢命令行？这两步同样可以手动完成：

```sh
niq project create my-project
niq project run my-project
```

单独运行 `niq project`（或 `niq project list`）是对所有项目的方向键选择器 —— 每行显示状态、端口和目录：`↑`/`↓`、`⌃n`/`⌃p` 或 `j`/`k` 移动，`enter` 选中，然后启动（若已停止）或停止 / 重启（若在运行）。一个实例由它的端口来标识，所以 CLI、WebUI，以及你在终端里手工启动的项目操作的都是同一个东西 —— 控制面并不保存自己启动过什么的列表。没有终端时，选择器打印同样的表格；`--ids` 只打印 id。

| 命令 | 说明 |
| --- | --- |
| `niq` | 在后台启动控制面；若已在运行则报告其状态 |
| `niq -f`、`niq --foreground` | 在当前终端里运行控制面 |
| `niq status [--json]` | 控制面以及每个项目实例的状态（`--json` 供工具使用） |
| `niq stop [-p]` | 停止控制面（`-p` 同时停止正在运行的项目） |
| `niq restart [-p]` | 在启动时所用的地址上重启（`-p` 同时重启正在运行的项目） |
| `niq project`、`niq p` | 选一个项目：启动、停止或重启 |
| `niq update` | 用 npm 安装最新发布版 |
| `niq project start\|stop\|restart <id>` | 直接对某个实例操作，不经过选择器 |
| `niq project create` / `niq project run` | 创建一个项目 / 在当前终端里运行一个项目 |
| `niq completion install` | 把补全脚本放到 shell 已经在扫描的位置 |
| `niq completion <shell>` | 打印 bash、zsh 或 fish 的补全脚本 |

`niq update` 会向 npm 查询已发布版本，用 `npm install -g @niq.run/niq@latest` 安装，并提示你执行 `niq restart -p` —— 控制面和项目是独立进程，仍在运行旧的二进制。裸跑 `niq` 时，每天最多向 registry 查询一次，并在每次启动时（基于缓存的结果）提示有更新的版本，直到你装上为止；设置 `NIQ_NO_UPDATE_CHECK=1` 可关闭该检查。

补全不需要改任何 dotfile：`niq completion install` 会把脚本写进 shell 本来就会扫描的目录 —— zsh `$fpath` 中某个可写入的条目、`~/.config/fish/completions`，或 bash-completion 的用户目录 —— 并打印它放到了哪里。`make install`、安装脚本和 npm 包的 `postinstall` 都会调用它，所以正常安装之后，下一个 shell 里 Tab 补全就能用了。`NIQ_NO_COMPLETION=1` 可跳过这一步。

脚本也可以直接打印出来，用于那个目录不可用的场合（例如被锁死的 `/usr/share/zsh`，或由脚本启动的 shell）：

```sh
# bash
source <(niq completion bash)
# zsh
niq completion zsh > "${fpath[1]}/_niq"
# fish
niq completion fish | source
```

输出在终端里带颜色、否则为纯文本（设置了 `NO_COLOR` 时也是纯文本），因此管道和日志始终可读。

每个项目都在仅回环的端口上提供自己的 WebUI，所以不会被意外暴露出去：请通过控制面在 **http://localhost:9527/p/&lt;id&gt;/** 访问，它会代理到该项目。无论实例是否在运行，这个地址都可用 —— 打开一个已停止项目的地址，会落到项目列表并提示启动，而不是一个错误页。端口在首次运行时动态分配，之后按项目持久化，因此跨重启保持稳定（可用 `--bus` / `--webui` 覆盖）。`niq project list` 会显示每个项目的 `webui` 和 `bus` 端口。

### 服务端部署

部署时唯一需要暴露的端口就是控制面：它提供 SPA、项目管理 API，以及每个项目的 WebUI（位于 `/p/<id>/`）。项目与事件总线只绑定回环地址，因此即使部署在公网机器上，它们也不会从外部可达。

```sh
niq --addr :9527 --auth alice:s3cret
```

在暴露端口之前先配置凭据：此后来自任何非回环地址的请求都会在认证之前收到 401，而来自本机的请求仍然免密码。`--auth`（控制面）与 `--webui-auth`（手工启动的项目）读写同一个 `~/.niq/common/auth.json`，所以一对凭据就覆盖全部。没有配置凭据时，远程调用方会被完全挡在外面，niq 也会在启动时对绑定的地址发出警告。

Basic auth 会在每个请求上都发送凭据，因此在把 niq 暴露到公网之前，请在前置做 TLS 终止（或通过 SSH/WireGuard 隧道访问）—— niq 本身只讲明文 HTTP。

升级二进制之后，请 `niq restart` 控制面，并重启正在运行的项目：每个进程提供的是它构建时携带的页面和资源（资源 URL 是相对路径，所以项目页面通过 `/p/<id>/` 加载自己的 bundle），这意味着旧项目在重启之前会一直渲染旧版 UI。

### 模型提供方

`reason` worker 与 LLM 提供方通信。提供方设置（端点、模型、API Key）存放在 `~/.niq/common/providers/provider.json`；`api_key` 和各个 header 的值支持环境变量展开 —— 例如 `"api_key": "${OPENAI_API_KEY}"` 可以让密钥不落到存储文件里。

## 菜单栏（macOS）

同一份报告，一次点击即可查看。

```sh
make build miq   # 构建 dist/niq.app 和 ./bin/miq
miq              # 把 niq 放进菜单栏；再次运行则聚焦它
```

在 macOS 上执行 `npm install -g @niq.run/niq` 就已经给了你全部三样东西：`niq`、`miq`，以及 `~/Applications` 里的 `niq.app`。`make build miq` 是给检出目录用的。

图标是 Web UI 的 logo 作为模板图像：菜单栏自身文字颜色的一块方块，其中“品”字被镂空。macOS 在深色菜单栏上把模板画成白色、在浅色菜单栏上画成黑色，所以图标会跟随外观变化，无需同步任何图纸。

菜单里只有动作，别无他物：**Open WebUI** 和 **Stop**（控制面未运行时显示 **Start**），然后是每个项目一项 —— `pin · running` —— 每项都是一个 **Open WebUI / Start / Stop / Restart** 子菜单，该实例无法执行的动作会置灰。**Open GitHub** 和 **Quit** 用于关闭菜单。

发布版以 `niq_menubar_darwin_universal.zip` 的形式提供通用构建，内含 `niq.app` 和 `miq`。它是 ad-hoc 签名的，未经过公证，所以首次启动需要右键 → 打开（或者执行 `xattr -d com.apple.quarantine niq.app`）。

菜单打开时会自行刷新，因此没有需要惦记的 Refresh 项。它只是一个前端，而非第二套实现：状态来自 `niq status --json`，每个动作都是一条 `niq` 命令（`niq project restart <id>`、`niq restart -p`、……），所以 app 不会与 CLI 产生偏差。`miq` 是一个 shell 脚本，它优先寻找与自身同目录的 bundle —— 也就是本次构建产出的那个 —— 然后是 `NIQ_APP`、`~/Applications` 和 `/Applications`，并把旁边的 `niq`（`--niq <path>`）交给 app，因为 GUI 启动不会继承 shell 的 PATH。

## 项目结构

```
core/itfs/ 接口与类型（契约，而非实现）
core/impl/ 实现（workers、bus、transports、providers、hosts）
app/       niq 产品本体：控制面、项目运行时、WebUI
app/publish/cmd  CLI 入口（package main）
app/publish/npm  npm 分发（启动器 shim + 包模板）
ext/       外部 worker 实现（HTTP）
```

## 构建与测试

```sh
go build ./... && go vet ./...
go test ./core/impl/eventbus/ -count=1
```

WebUI bundle 是被提交进仓库的（它以 `go:embed` 嵌入），`make build niq` 完全按现状取用它：重建 SPA 是单独的一步。单纯的 `make build` 会按顺序把两者都做 —— 先 bundle，再 CLI，再 app。该提交对 `go build ./...`、`go test ./...` 和发布流程仍然重要（goreleaser 和 CI 不跑 Node），所以请把重建后的 `app/webui/assets/dist` 与改动一起提交：

```sh
make build              # 这台机器能构建的一切
make build webui        # 只构建 bundle
make build niq          # 只构建 CLI，按现状嵌入 bundle
make build niq webui    # 先 bundle，再 CLI
make build miq          # 菜单栏 app
make install            # 软链已构建好的产物 —— 它不做构建
```

`build` 后面的词是目标而不是参数 —— make 会两者都跑 —— 所以单独的 `make niq`、`make webui`、`make miq` 也能用，而只有 `make build` 时仍然表示“全部”。把 `niq` 和 `webui` 一起写出时，无论你以什么顺序输入，都会先构建 bundle。

`make install` 从不构建：如果 `./bin/niq` 不存在，它会说明情况并停下，因此一次安装不会悄悄变成漫长的 npm/vite 运行。

### 前端开发

控制面运行起来之后，Vite 开发服务器（在 `app/webui/assets` 里执行 `npm run dev`，端口 5173）会把 API 流量代理到 :9527，因此两个页面都能以 HMR 工作：

- 控制面 UI：http://localhost:5173/
- 某个项目的 UI：http://localhost:5173/p/&lt;id&gt;/（该项目必须正在运行）

## 数据目录

运行时数据默认位于 `~/.niq/` 之下。设置 `NIQ_HOME` 环境变量可把这个根目录迁到自定义位置（用于可移植安装、测试，或把状态放在 home 目录之外）；值中的 `~`/`~/` 会被展开。`NIQ_AUTH_CONFIG` 和 `NIQ_PROVIDER_CONFIG` 仍然整体覆盖那两个具体文件，优先级高于 `NIQ_HOME`。

```
~/.niq/
  bin/                         预构建二进制（install.sh）
  niq.log                      控制面日志
  common/
    templates/                 项目模板
    providers/provider.json    共享的提供方配置（API Key 通过环境变量展开）
  projects/<id>/               每个项目一个目录
    id/identities.json         worker 身份注册表（由 bus 持有）
    programs/                  Program 存储
    logs/                      按天轮转的运行日志
```
