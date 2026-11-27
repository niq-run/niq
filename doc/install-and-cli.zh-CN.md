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

- **macOS**：把 `~/.niq/bin/niq` 指向本次安装的二进制，因为 GUI 启动不继承 PATH，
  也跑不了 node shim。包里已经不再携带 app —— 可安装的桌面应用有自己的渠道，见下文。
- **除 Windows 外的所有平台**：把补全脚本写进 shell 本来就会扫描的目录
  （见 [§3.6](#36-shell-补全)），所以下一个 shell 里 Tab 补全就能用，无需改 dotfile。

两者都是尽力而为：任何一项写不进去都不会让安装失败。`NIQ_NO_APP=1` 跳过 CLI 软链，
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
SPA 是单独的一步（`make build webui`）。单纯 `make build` 会按顺序做本机能做的一切 ——
bundle、CLI，以及装了 Rust 与 Tauri CLI 时的桌面应用 —— 目标名一起写时顺序不影响结果
（`make build niq webui` 仍然先构建 bundle）。

### 桌面应用（macOS、Linux、Windows）

可安装的应用是 `app/client/desktop` 里的套壳：它把 CLI 作为 sidecar 带在包里，拉起网关，
再打开网关自带的 WebUI，所以装一个东西就得到一个能用的 niq。它是单独的分发渠道 ——
npm 包与 CLI 归档里都不包含它。

```sh
make desktop           # 为本机构建
make install           # 把 niq.app 复制进 /Applications（用 APP_DIR=… 改位置）
make dist-desktop      # 发布产物：.dmg + 打包的 .app（UNIVERSAL=1 出双架构）
```

发布时由 `app/publish/desktop` 按平台挂上去（流水线见
`app/publish/desktop/README.md`）。没有 Developer ID 的 macOS 构建是 ad-hoc 签名，
所以下载来的副本会被 Gatekeeper 拦住，直到清掉隔离属性；DMG 里附带的 `INSTALL.txt`
写了那一行命令。

macOS 原生客户端与其它的三方客户端在 clients 仓库
（`github.com/niq-run/clients`），不在本仓库。

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
worker 的回答以一条**点在行首顶格**的分隔线开头（`niq ·········`，名字贴最左），下面空一行，
再是正文，按 **markdown** 渲染 —— 标题、
粗体/斜体、行内代码、链接、列表、引用、代码块（go/python/js-ts/json/shell/rust 会做语法高亮）、
管道表格、分隔线 —— 左对齐折行。表格会按终端宽度收缩并折行单元格，而不是溢出屏幕。
上下文信息则压缩成紧凑的单行：`thinking` 每条只占一行 —— 前面是**彩色标题**加冒号，后面是暗色
详情，超出宽度用省略号截断。**工具调用则各自变成一张卡片**，沿用 pi 的形状：第一行是调用 ——
能力名、目标 worker、参数 payload，放不下就省略；下面留一空行，再接**最多两行**返回，颜色更浅。
返回会合进调用那张卡里，所以一次调用连同它的返回是一块，而不是一长条行；只有用户的 `you ›` 和
worker 的 `id ›` 保留箭头，普通事件行用更朴素的 `标题:` 形式。

标题用颜色区分，方便扫读：用户的 `you` 是绿色，worker 回答沿用品牌蓝，`thinking` 是灰色，
警告类（`interrupted`、`abort`、`timeout`、`cancel`）是亮黄色、铺在独立的**琥珀色底**上，
失败是红色、铺在错误底色上。

结构靠背景色分层（参考 pi）：用户消息铺一层淡色卡片（**不再加下划线**，注入的 `<system-reminder>`
会被隐藏），**每次工具调用是它自己的一张绿色卡片** —— 亮色的调用行、一个空行、然后再用浅一点的
颜色写返回，底色是偏灰的深绿 —— 代码块有代码底色，输入框由上下两条分割线框住（**本身不铺底色**），
**面板上方留一空行**。worker 的回答**不铺背景色**：分隔线顶到最左，正文整体左缩进一格，
让正文和那些卡片里的文字左对齐。thinking 不加背景、只用灰字，**最多 10 行且边生成边刷新**；
超过 10 行时保留前 3 行与最新 7 行，中间用 `··· 省略 N 行 ···` 提示。每个背景块左右留边、
**上下各留一行内边距**，块与块之间再空一行，所以聊天记录是分层的，而不是一整片文字。发送目标、
视图范围、输入模式和 vim 状态直接画进底部面板的**上边框**（左侧 `target: <id> · view: all · mode: default`，
右侧 `insert · help`），既常驻又不占聊天的行。背景在 256 色下降级，`NO_COLOR` 或 16 色终端下则不显示。色板不是猜的，而是**问终端**：
启动时查询背景色（OSC 11）和明暗方案（`CSI ? 997 n`），并用 mode 2031 订阅主题切换，所以浅色终端会拿到
浅色面板，切换主题时实时重绘；都不支持时退回 `COLORFGBG`，再退回深色。
滚轮每格滚动一行；每条已发送的消息在行首记下收件人（`you → <id> › …`），
流断开时会按退避策略重连，状态显示在边框里。

因为视图接管了鼠标（滚轮与拖拽上报），终端自身的选区在里面是关闭的。在聊天记录上拖拽即可选中文本，
松开即复制 —— 支持 OSC 52 的终端直接复制，否则在 macOS 上走 `pbcopy`、Linux 上走
`wl-copy`/`xclip`/`xsel`。选区用反色显示，并且滚动后依然保持。

输入框是**独立组件**，形状和 WebUI 的输入框保持一致。它支持多行：`shift+enter`（需要终端支持
Kitty 键盘协议）、`⌃J` 或 `⌥enter` 换行，`enter` 发送。大段粘贴在屏幕上是**一个芯片** ——
`[pasted text #1 · 24 lines]` —— 只有发送时才展开成真实文本，所以贴一段堆栈不会把对话顶走，
也不会跟光标打架。粘贴文件路径（或把文件拖进终端，多数终端会粘贴成路径）会把它变成附件：
图片走 base64 内联，其他文件是**引用路径**还是**上传**取决于网关在哪（见下）。
附件和多行消息显示在输入框上方，有上限，输入区会滚动保持光标可见。`⌃X` 打开输入模式选择弹窗
（default / append / interrupt / schedule，就是 WebUI 模式选择器发的 `input_mode`），点击上边框的
`mode:` 段也能打开；当前模式和附件数量画在底部面板的上边框里。

`file` 块本质上只是**引用**：reason worker 把它变成一句带路径的注记
（`[referenced file: … (N bytes)]`），并不读内容 —— 真正读文件的是在项目所在机器上运行的工具。
所以路径必须对**网关那台机器**有效，这就是要区分本机还是远程的原因。网关在本机时（回环地址，
或它自己的主机名），附件保留本地路径：不拷贝，工具能直接打开。不在本机时，发送前会先通过
`POST /api/upload` 上传，消息里引用的是网关返回的路径；边框会显示 `(upload)` 以示区别。
`NIQ_ATTACH=upload` 或 `NIQ_ATTACH=path` 可以强制选择 —— SSH 隧道连远程网关看起来像本机，
共享挂载又会让远程机器表现得像本机。图片两种情况都例外：始终以 base64 内联，是模型真正能看到的
多模态块。

| 按键 | 动作 |
|---|---|
| `enter` | 发送消息 |
| `shift+enter`、`⌃J`、`⌥enter` | 换行（多行消息） |
| `←` / `→` / `↑` / `↓` | 移动光标 |
| `⌃A` / `⌃E` | 行首 / 行尾 |
| `⌃B` / `⌃F`、`⌥b` / `⌥f` | 左 / 右移一个字符；按词移动 |
| `⌃P` / `⌃N` | 上一行 / 下一行 |
| `⌫` / `⌦` | 删除光标前 / 后的字符 |
| `⌃K` | 删除到行尾 |
| `⌃W` | 删除前一个词 |
| `⌃Y` | 粘回最后一次删除的内容 |
| `⌃?` | 快捷键帮助 |
| `esc` | 进入普通模式 |

对话视图是**模态**的（类似 vim）：打开时是**插入模式**，按键都进消息；`esc` 进入**普通模式**，
按键只用来滚动聊天记录；`i` 或点击输入框回到插入模式。输入框上方的边框左侧是发送目标、视图范围和
输入模式，右侧是 vim 状态和一个 `help` 入口（快捷键提示不在边框上，放在帮助卡片里）。边框上每一段
都可以点击：`target:`、`view:`、`mode:` 各打开对应的选择弹窗，`help` 打开快捷键卡片，
点击 vim 状态则在 insert / normal 之间直接切换（只有两个状态，不用弹窗）。
普通模式下按 `?` 或任意模式下按 `⌃?` 也能打开帮助。所有弹窗都按 `esc` 或在弹窗外部点击关闭
（关闭卡片绝不会连带退出对话，退对话仍需 `⌃C` 再按一次）。

| 普通模式 | 动作 |
|---|---|
| `j` / `k`、`⌃E` / `⌃Y` | 下 / 上滚动一行 |
| `⌃D` / `⌃U`、`⌃F` / `⌃B`、`PgUp` / `PgDn`、`⌃↑` / `⌃↓` | 上下翻页（到顶会加载更早的历史） |
| `g` / `G` | 跳到最早 / 最新 |
| `i` | 回到插入模式 |
| `?` | 快捷键帮助（覆盖在对话之上的卡片；`esc` 或点击弹窗外部关闭） |
| `shift+↑` / `shift+↓` | 上 / 下滚动一行 |

| 两种模式通用 | 动作 |
|---|---|
| `⌃X` | 输入模式选择弹窗：default / append / interrupt / schedule |
| `⌃T` / `⌃V` | target / view 选择，范围与 WebUI 一致（reason/coding/niq）（`↑`/`↓`、`j`/`k`、`⌃P`/`⌃N`、`enter`、`esc`；过长会自动翻页） |
| 鼠标滚轮 | 每次滚动一行 |
| 鼠标拖拽 | 选中文本，松开即复制 |
| `⌃C`、`⌃Q` | 退出对话 |

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
| `NIQ_NO_APP=1` | npm `postinstall` | 不为 GUI 客户端创建 `~/.niq/bin/niq` 软链 |
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
