# 安装、升级与 CLI

> 状态：as-built（2026-11-21）。简体中文 · [English](install-and-cli.md)。
>
> 怎么把 `niq` 装上一台机器、怎么往前走、以及每条命令与每个参数做什么。
> 目录划分见 [directory-layout.zh-CN.md](directory-layout.zh-CN.md)；产品定位见
> [README](README.zh-CN.md)。

## 1. 安装

三种方式任选其一，都会把 `niq` 命令放进 PATH。**在你运行它之前什么都不会启动** ——
安装不会顺手拉起守护进程。

三种方式都不需要 JS 运行时：program worker 在内嵌的 QuickJS 引擎
（`modernc.org/quickjs`，纯 Go 实现）里运行脚本程序（PTC），因此不会拉起 `node`
进程，脚本也只能看到 worker 注入的 SDK —— 没有 `process`、`require`、`fetch`
或动态 `import()`。

### npm（推荐）

```sh
npm install -g @niq.run/niq
```

包内带有 macOS、Linux 与 Windows 的预编译二进制（x64 与 arm64），并在命令之外多做两件事：

- **macOS**：把 `niq.app` 复制到 `~/Applications`（Spotlight 与 Finder 都能看到），
  `miq` 用于打开菜单栏应用（与双击 app 是同一个菜单）；同时把 `~/.niq/bin/niq`
  指向本次安装的二进制，因为 GUI 启动不继承 PATH，也跑不了 node shim。
- **除 Windows 外的所有平台**：把补全脚本写进 shell 本来就会扫描的目录
  （见 [§3.6](#36-shell-补全)），所以下一个 shell 里 Tab 补全就能用，无需改 dotfile。

两者都是尽力而为：任何一项写不进去都不会让安装失败。`NIQ_NO_APP=1` 跳过 app，
`NIQ_NO_COMPLETION=1` 跳过补全。

这种方式靠 `niq update` 保持最新 —— 见 [§2](#2-升级)。

### 安装脚本（不依赖 npm）

```sh
curl -fsSL https://raw.githubusercontent.com/niq-run/niq/main/app/publish/cmd/install.sh | sh
```

它识别 `linux`/`darwin` 与 `amd64`/`arm64`，下载该版本的
`niq_<os>_<arch>.tar.gz`，把二进制装到 `~/.niq/bin`，验证它能跑，安装补全，并在该目录
不在 `PATH` 上时把它写进你的 shell rc 文件。

| 变量 | 作用 |
|---|---|
| `NIQ_INSTALL_DIR` | 装到 `~/.niq/bin` 之外的位置 |
| `NIQ_VERSION` | 安装指定 release 标签，而不是 latest |
| `NIQ_NO_MODIFY_PATH=1` | 不改 rc 文件，PATH 由你自己加 |
| `NIQ_NO_COMPLETION=1` | 跳过补全安装 |

需要 `curl`（或 `wget`）与 `tar`。该脚本不支持 Windows —— 用 npm。

### 从源码

需要 **Go 1.26+**（内嵌 JS 引擎的要求）与 **Node 20+**（打包 WebUI 用）。

```sh
git clone https://github.com/niq-run/niq
cd niq
make install
```

`make install` 会重建 WebUI bundle、构建 `./bin/niq`，再把它软链到
`~/.local/bin`（`PREFIX=/some/bin` 可指定其他目录），所以只要该目录在 PATH 上，
`niq` 在任何 shell 里都能用。它绝不凭空构建：`./bin/niq` 不存在时会直接告诉你并停下。

如果你更希望把二进制留在 `./bin/niq` 并从检出目录直接调用，改用 `make build`。

bundle 是提交进仓库的（`go:embed` 嵌入），`make build niq` 完全按现状取用它：重建
SPA 是单独的一步（`make build webui`）。单纯 `make build` 会按顺序全做 —— bundle、
CLI、app —— 目标名一起写时顺序不影响结果（`make build niq webui` 仍然先构建 bundle）。

### 菜单栏（macOS）

菜单栏应用属于 macOS 安装的一部分，不是单独的分发渠道：npm 包会把 `niq.app` 复制到
`~/Applications`，并把 `miq` 放进 PATH（检出目录用 `make build miq` 构建这两样）。
`miq` 负责打开它；再次运行则聚焦菜单。

```sh
miq              # 把 niq 放进菜单栏；再次运行则聚焦它
```

发布构建（`niq_mac_darwin_universal.zip`）是 ad-hoc 签名、未公证，所以新下载的
`niq.app` 首次需要右键 → 打开，或执行 `xattr -d com.apple.quarantine niq.app`。

### 验证

```sh
niq --version      # 打印版本（有构建时间则一并打印）后退出
```

## 2. 升级

升级的单位是二进制：网关与项目进程都继续运行它们启动时的那份，所以**下面每条路径都以
`niq restart -p` 收尾**。

| 安装方式 | 升级方式 | 说明 |
|---|---|---|
| npm | `niq update` | 执行 `npm install -g @niq.run/niq@latest`，走 npm registry |
| 安装脚本 | 重跑那行 `curl` | 取 GitHub 最新 release；`NIQ_VERSION=x.y.z` 可固定版本 |
| 源码 | `git pull && make install` | 重建 bundle 与 CLI，替换软链 |

收尾：

```sh
niq restart -p
```

`-p` 会同时重启正在运行的项目。之所以必须重启，是因为资源 URL 是相对路径：项目页面
加载的是**它自己**的 bundle，所以没重启的实例会一直渲染旧版 UI，哪怕新二进制已经在磁盘上。

### 被动的更新检查

裸跑 `niq` 时，每天最多向 npm registry 查询一次（结果缓存在 `~/.niq/update-check.json`），
有新版本时在输出末尾提示 —— 每次启动都提示（来自缓存），直到你装上为止。这个检查刻意做得很克制：
2 秒超时，stdout 不是终端时跳过，自身的失败从不报告。`NIQ_NO_UPDATE_CHECK=1` 可关闭它。

固定版本或回退：安装脚本用 `NIQ_VERSION=0.14.0`，npm 用
`npm install -g @niq.run/niq@0.14.0`。

## 3. CLI

### 3.1 一段话理解模型

`niq` 分两层。**网关**是一个常驻进程，监听一个端口（默认 `127.0.0.1:9527`），
是你唯一需要对外暴露的 HTTP 端点：它提供 WebUI、项目管理 API，以及每个项目在
`/p/<id>/` 下的页面。**项目**是一个隔离的智能体实例 —— 自己的事件总线、自己的 worker
集合、自己的 WebUI —— 作为**独立进程**运行在自己的回环端口上。所以关掉网关不会停掉项目，
重启项目也不会碰网关。实例由持久化的端口来识别，而不是由谁启动的：WebUI、选择器、以及你
手工启动的项目操作的都是同一个东西。

CLI 没有项目命令：创建、启动、停止、重启都在 WebUI 与选择器（`niq i`）里，两者是同一个
HTTP API 的客户端。CLI 的职责是确保网关在跑，并告诉你现在什么在跑。

### 3.2 第一次运行

```sh
niq
```

它会让网关**脱离终端**在后台启动，打印可访问的地址，然后回到提示符 —— 关掉 shell
（或断开 SSH 会话）不再会把它停掉。再次运行 `niq` 是汇报而不是再起一个：它完全等同于
`niq status`，所以一条命令同时覆盖“启动它”和“它是否在运行？”。

```
[gateway] running  pid 4711 · since 2026-11-01 23:57
webui    http://localhost:9527
log      ~/.niq/niq.log
stop     niq stop
restart  niq restart

[projects]  niq i to start, stop or restart one
[pin]    running  webui 57609  bus 57608  http://localhost:9527/p/pin/
[notes]  stopped  webui 57621  bus 57620  http://localhost:9527/p/notes/
```

项目地址无论实例是否在运行都可用：打开已停止项目的 `/p/<id>/` 会落到启动提示，而不是
错误页。表格里端口连不上，就表示该实例没在运行。

打开 **http://localhost:9527** 就是 WebUI：项目列表、从模板创建、启动/停止/重启，
全程不需要终端。

### 3.3 命令

| 命令 | 作用 |
|---|---|
| `niq` | 确保网关在后台运行，然后打印状态表 |
| `niq w [-f]` | 确保网关在跑，打开浏览器，并打印地址作为兜底 |
| `niq i` | 确保网关在跑，进入交互式终端选择器 |
| `niq c [project]` | 确保网关在跑，在终端里和某个项目对话（只有一个项目时默认选它） |
| `niq status [--json\|--ids]` | 网关与每个项目实例；读本地快照，不启动守护进程 |
| `niq stop [-p]` | 停止网关（`-p` 同时停止正在运行的项目） |
| `niq restart [-p]` | 在启动时所用的地址上重启（`-p` 同时重启正在运行的项目） |
| `niq update` | 用 npm 安装最新发布版 |
| `niq completion install` | 把补全放到 shell 本来就会扫描的位置 |
| `niq completion <shell>` | 打印 bash、zsh 或 fish 的补全脚本 |
| `niq --version` | 打印版本后退出 |
| `niq --help` | 打印用法 |

`-p` / `--projects` 的存在正是进程模型的直接结果：项目比网关活得久，所以单纯的
`niq stop` 会让它们继续在自己的回环端口上提供服务。

### 3.4 启动参数

参数可以放在任何“启动”的位置 —— `niq --addr :8080`、`niq w --addr :8080`，
`niq restart` 会复用已记录的地址。

| 参数 | 默认 | 含义 |
|---|---|---|
| `--remote` | 关 | **本次**监听所有网口；需要 `--auth` 或已保存的凭据 |
| `--local` | 关 | **本次**只监听回环，忽略已记住的远程地址 |
| `--addr <host:port>` | 已记住的偏好，否则 `127.0.0.1:9527` | 显式监听地址；显式绑到非回环地址才是“对外” |
| `--auth <user:pass>` | 读 `~/.niq/common/auth.json` | 对非回环来源的 Basic auth；设置后会持久化到该文件 |
| `--foreground`、`-f` | 关 | 附着在当前终端里运行（systemd、容器、`docker run`，或只是想盯着看） |

`niq -f` 与 `niq w -f` 是同一件事：前台网关。在没有终端可脱离的场合，这就是 supervisor
应该用的形式。

`--remote` / `--local` 是**单次**开关：只影响这次启动，且保留端口（有偏好就用偏好的端口，
否则 9527）。二者互斥，也不能与 `--addr` 同用（一个地址只说一次）。它们可以代替下面的
交互询问，更适合脚本 —— 如果某台服务应该永远是远程，把 `--remote` 写进 unit 文件，
或者在询问时答一次并让它被记住。

#### 绑定范围，以及记住这个选择

选定绑定地址有三种方式，按优先级排列：

1. **在命令行直接说**：`niq --remote`、`niq --local` 或 `niq --addr <host:port>`。
   只对本次生效，不写任何文件。
2. **已记住的偏好**（`~/.niq/common/config.json`）。
3. **询问**：上面两者都没有时，在终端里问一次。

所以既没传 `--addr`、也没传 `--remote`/`--local`、也没有已记住的偏好时，终端里启动会问：

```
who may reach the gateway?
  [1] this machine only  — 127.0.0.1:9527 (loopback)
  [2] all interfaces — other machines can open the WebUI
choice [1/2, Enter=1]:
```

随后会问是否记住这次选择。回答**否**是明确的“每次单独决定”：下次启动还会再问。回答**是**会写入
`~/.niq/common/config.json`：

```json
{ "default_addr": ":9527" }
```

优先级是 **显式参数 > 已记住的偏好 > 回环默认**，所以一次性的 `niq --addr …`（或
`--remote`/`--local`）不会改掉你保存的设置。网关本身没变：它仍然默认回环，只绑定 CLI
传进去的地址。想重新被询问，删掉 `config.json`（或其中的 `default_addr` 键）即可。

对外绑定需要认证。`--remote` 在没凭据时会**直接报错**并说明怎么补
（`niq --auth user:pass --remote` 一步到位）；在询问时选“all interfaces”则是说明同样的事、
本次继续用回环且不记住。之前保存的偏好如果事后凭据被删，也会被跳过并给出提示。
显式的 `--addr` 仍然照绑（那是一条直接指令），由网关警告未认证。

状态行总是写明当前暴露范围，不需要猜：

```
webui    http://localhost:9527  loopback only
remote   off — needs auth first: niq --auth user:pass
```

当网关确实对外可达时，第二行会变成另一台机器可以打开的 URL。

### 3.5 给脚本用的状态

`niq status --json` 打印与表格同源的快照：

```json
{
  "version": "0.16.0",
  "gateway": {
    "running": true,
    "pid": 4711,
    "addr": "127.0.0.1:9527",
    "url": "http://localhost:9527",
    "log": "/Users/me/.niq/niq.log",
    "started": "2026-11-01T23:57:12Z"
  },
  "projects": [
    {
      "id": "pin",
      "running": true,
      "webui": 57609,
      "bus": 57608,
      "url": "http://localhost:9527/p/pin/",
      "log": "/Users/me/.niq/projects/pin/logs/niq-2026-11-01.log"
    }
  ]
}
```

`niq status --ids` 只打印项目 id，一行一个。两种形式都读本地快照（`~/.niq/shell.json`
加上各项目目录），所以网关没起来也能用 —— macOS app 与 shell 补全调用的正是它们。

### 3.6 Shell 补全

```sh
niq completion install                     # 识别 shell 并写入脚本
niq completion install --shell zsh         # 跳过识别（SHELL 未设置时，例如脚本里）
```

它写进 shell 本来就会扫描的目录，并打印写到哪了：

- **zsh**：问 `zsh` 要 `$fpath`，取最合适的可写入条目（Homebrew 的 `site-functions`，
  或 oh-my-zsh 的 `custom/completions`）；
- **fish**：`~/.config/fish/completions/niq.fish`；
- **bash**：`~/.local/share/bash-completion/completions/niq`。

不需要改任何 dotfile。若找不到可用目录（被锁死的 `/usr/share/zsh`、由脚本启动的 shell），
就直接打印脚本：

```sh
# bash
source <(niq completion bash)
# zsh
niq completion zsh > "${fpath[1]}/_niq"
# fish
niq completion fish | source
```

`make install`、安装脚本与 npm 包的 `postinstall` 都会执行 `completion install`，
所以正常安装之后就自带 Tab 补全。`NIQ_NO_COMPLETION=1` 可跳过。

### 3.7 输出

stdout 是终端时带颜色，否则为纯文本（设置了 `NO_COLOR` 或 `TERM=dumb` 时也是纯文本），
因此管道与日志始终可读。

### 3.8 选择器按键

`niq i` 对每个项目打开方向键选择器 —— 每行显示状态、端口与目录：

| 按键 | 动作 |
|---|---|
| `↑` / `↓`、`⌃p` / `⌃n`、`k` / `j` | 移动 |
| `enter` | 选中（运行中则进入对话，或启动 / 停止 / 重启） |
| `q`、`Esc`、`⌃C` | 退出 |

没有终端时，选择器改为打印同样的表格。

### 3.9 终端对话

`niq c`（或选择器里的 **Chat** 动作）在终端里打开与某个项目的对话 —— 和 WebUI 面对的是
同一个事件总线，只是换了一个客户端。它把消息以人类 interface worker 的身份发出，并把
实时事件流逐个打印出来。

视图是全屏的（终端备用缓冲区）：聊天记录填满屏幕并滚动，输入框固定在最底部。历史通过项目的
`/api/events/before` 接口翻页：连上时先加载一页，滚到顶部时会再取更早的一页。**worker 菜单**
（`⌃T`）可以把视图只看某一个 worker（任意 worker，包括工具），若是对话伙伴还会同时把下一条消息
发给它；选 **all** 清除过滤。项目的启动与停止仍在选择器或 WebUI 里完成。

滚动窗口与鼠标选区都是**独立组件**：`viewport` 只负责内容行数、可视高度、首行和是否贴底，
`selection` 只负责把渲染行上的单元格区间映射成剪贴板文本。翻页键、单行键、滚轮、以及拖到边缘时的
自动滚动都走同一个 viewport，因此无论怎么触发，翻页和加载历史的语义都一致。

用户自己的消息保持内联在 `you › …` 这一行，并且是唯一会在上方留一空行的块。
worker 的回答把说话人标签单独一行，**上方不留空行**，正文在下面按 **markdown** 渲染 —— 标题、
粗体/斜体、行内代码、链接、列表、引用、代码块（go/python/js-ts/json/shell/rust 会做语法高亮）、
管道表格、分隔线 —— 左对齐折行。表格会按终端宽度收缩并折行单元格，而不是溢出屏幕。
上下文信息则压缩成紧凑的单行：`thinking`、工具调用、请求结果都只占一行 —— 前面是**彩色标题**加
冒号，后面是暗色详情，超出宽度用省略号截断。只有用户的 `you ›` 和 worker 的 `id ›` 保留箭头，
事件行用更朴素的 `标题:` 形式。

标题用颜色区分，方便扫读：用户的 `you` 是绿色，worker 回答沿用品牌蓝，`thinking` 是品红，
普通事件（工具调用、请求结果）是青色，警告类（`interrupted`、`abort`、`timeout`、`cancel`）
是黄色，失败是红色。

结构靠背景色分层（参考 pi）：用户消息铺一层淡色卡片（**不再加下划线**，注入的 `<system-reminder>`
会被隐藏），工具调用与结果用**绿色面板**，代码块有代码底色，输入框放在由分割线框住的底部面板里，
**面板上方留一空行**。thinking 不加背景，只显示一行灰色文字，自然退到次要位置。每个背景块左右留边、
**上下各留一行内边距**，块与块之间再空一行，所以聊天记录是分层的，而不是一整片文字。发送目标、
视图范围、滚动位置和快捷键直接画进底部面板的**上边框**（`── → <id> · view: all ────── ctrl+u/d … ──`），
既常驻又不占聊天的行。背景在 256 色下降级，`NO_COLOR` 或 16 色终端下则不显示；色板读 `COLORFGBG`，在
浅色终端上用浅色面板。滚轮每格滚动一行；每条已发送的消息在行首记下收件人（`you → <id> › …`），
流断开时会按退避策略重连，状态显示在边框里。

因为视图接管了鼠标（滚轮与拖拽上报），终端自身的选区在里面是关闭的。在聊天记录上拖拽即可选中文本，
松开即复制 —— 支持 OSC 52 的终端直接复制，否则在 macOS 上走 `pbcopy`、Linux 上走
`wl-copy`/`xclip`/`xsel`。选区用反色显示，并且滚动后依然保持。

| 按键 | 动作 |
|---|---|
| `enter` | 发送消息 |
| `←` / `→`、`⌃A` | 移动光标；`⌃A` 跳到行首 |
| `⌫` / `⌦` | 删除光标前 / 后的字符 |
| `⌃K` | 清空当前行 |
| `⌃U` / `⌃D`、`⌃B` / `⌃F`、`PgUp` / `PgDn` | 上下翻页（到顶会加载更早的历史） |
| `⌃Y` / `⌃E`、`⌃P` / `⌃N` | 上 / 下滚动一行 |
| 鼠标滚轮 | 每次滚动一行 |
| 鼠标拖拽 | 选中文本，松开即复制 |
| `⌃T` | worker 菜单：选择视图范围并设定发送目标（`↑`/`↓`、`j`/`k`、`⌃P`/`⌃N`、`enter`、`esc`） |
| `⌃C` | 退出对话 |

## 4. 服务端部署

部署时唯一需要暴露的端口就是网关：它提供 SPA、项目管理 API，以及每个项目的 WebUI
（位于 `/p/<id>/`）。项目的 HTTP API 只绑回环地址，因此即使部署在公网机器上，它们也不会
从外部可达；事件总线监听所有接口，但对每个连接都用签名 worker token 认证。

```sh
niq --addr :9527 --auth alice:s3cret
```

在终端里也可以不写参数：裸 `niq` 会询问绑回环还是所有网口，并询问是否记住（见 §3.4）。
但完全没有凭据时仍需要先配好认证，所以 `--auth`（或手写 `~/.niq/common/auth.json`）
是唯一不能省的一步 —— 否则提示会告诉你该配什么，并继续使用回环。

认证是**可选**的。配置了凭据之后，来自任何非回环地址的请求在认证前都会收到 401，
而来自本机的请求仍然免密码。`--auth` 会持久化到 `~/.niq/common/auth.json`，所以一对凭据
就覆盖网关以及它代理的一切。

没有配置凭据时，服务端什么也不强制：任何能连上它的调用方都会被服务，网关也会在启动时说明
这一点。这正是它默认只监听回环的原因 —— 用显式的 `--addr`（最好再加 `--auth`）才接受远程连接。

Basic auth 会在每个请求上发送凭据，因此在把 niq 暴露到公网之前，请在前置做 TLS 终止
（或通过 SSH/WireGuard 隧道访问）—— niq 本身只讲明文 HTTP。

若想让网关附着在终端里而不是脱离 —— systemd、容器、`docker run`，或只是想看日志：

```sh
niq w --foreground
```

## 5. 环境变量

| 变量 | 使用方 | 作用 |
|---|---|---|
| `NIQ_HOME` | 全部 | 迁移根目录（默认 `~/.niq`）；`~`/`~/` 会被展开 |
| `NIQ_AUTH_CONFIG` | 网关 | 整体覆盖 `common/auth.json` |
| `NIQ_PROVIDER_CONFIG` | launcher、project、worker | 整体覆盖 `common/providers/provider.json` |
| `NIQ_NO_UPDATE_CHECK=1` | `niq` | 关闭每日 registry 检查 |
| `NIQ_NO_COMPLETION=1` | 安装器、`niq completion install` | 跳过补全 |
| `NIQ_NO_APP=1` | npm `postinstall` | 不把 `niq.app` 复制到 `~/Applications` |
| `NIQ_INSTALL_DIR`、`NIQ_VERSION`、`NIQ_NO_MODIFY_PATH` | 安装脚本 | 见 [§1](#安装脚本不依赖-npm) |
| `PREFIX` | `make install` | 软链目标目录（默认 `~/.local/bin`） |
| `NO_COLOR`、`TERM=dumb` | 每条命令 | 纯文本输出 |

## 6. 它往哪里写

除非 `NIQ_HOME` 另有指定，一切都在 `~/.niq` 之下：

```
~/.niq/
  shell.json                   网关快照（pid、地址），`niq status` 读它
  niq.log                      网关日志
  update-check.json            缓存的“是否有新版本”答案
  bin/niq                      安装脚本 / npm：GUI 启动也能找到的二进制
  common/auth.json             Basic auth 凭据（网关所有）
  common/templates/            项目模板
  common/providers/provider.json   共享 provider 配置（API key 走环境变量展开）
  projects/<id>/               每个项目一个目录
```

`--auth` 写 `common/auth.json`；网关从不写项目文件，项目只写自己的 state、日志与身份注册表。
完整的运行时布局见 `design/project/README.zh-CN.md`。
