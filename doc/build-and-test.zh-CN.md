# 构建与测试

> 状态：as-built（2026-11-21）。简体中文 · [English](build-and-test.md)。
>
> 怎么在本机构建二进制、怎么跑 Go 与 TypeScript 测试、怎么带热更新开发 WebUI、
> 以及怎么在本地试跑发布流水线。安装见 [install-and-cli.zh-CN.md](install-and-cli.zh-CN.md)；
> 代码该放哪见 [directory-layout.zh-CN.md](directory-layout.zh-CN.md)。

## 1. 前置条件

| 工具 | 版本 | 用于 |
|---|---|---|
| Go | 1.26+（`go.mod` 写的是 `1.26.0`） | 全部；内嵌 QuickJS 引擎要求如此 |
| Node | 20+ | 构建 WebUI bundle、TypeScript SDK |
| Xcode command line tools | 近期版本即可 | 仅 macOS 菜单栏应用（Swift + `lipo`） |

只装 Go 就足够构建、vet 与测试后端：**编译 CLI 不需要 Node**，因为 SPA 是作为已提交文件
嵌入的。

## 2. 一条最容易踩的规则

WebUI bundle 是**提交进仓库**的（`app/client/webui/assets/dist`，由 `app/client/webui`
用 `go:embed` 嵌入）。因此：

- `go build ./...` 与 `make build niq` **不需要 Node，也不需要 `npm ci`** —— 它们完全按磁盘
  上的 bundle 原样取用；
- 重建 bundle 是单独的一步：`make build webui`（或在 `app/client/webui/assets` 里
  `npm run build`）；
- 单纯的 `make build` 会按顺序做本机能做的一切：bundle → CLI → app（app 部分仅 macOS）。

因为 bundle 是编译进去的，前端改动只有在**重建 bundle 并重建二进制**之后才可见；而这次
提交对 `go build ./...`、`go test ./...` 与发布流程仍然重要（goreleaser 与 CI 不跑 Node），
所以请把重建后的 `app/client/webui/assets/dist` 和改动一起提交。

## 3. make 目标

```sh
make build              # 本机能构建的全部：webui、niq、desktop（有 Rust 时）
make build webui        # 只构建 bundle
make build niq          # 只构建 CLI，按现状嵌入 bundle
make build niq webui    # 先 bundle 再 CLI —— 词序不影响结果
make install            # 把已构建的产物软链到 $(PREFIX)；从不构建
```

`build` 后面的词是**目标，不是参数** —— make 会把它们都跑一遍 —— 所以 `make niq`、
`make webui` 单独用也可以，而只有 `make build` 时仍然表示“全部”。当 `niq` 与
`webui` 一起写时，无论顺序如何都会先构建 bundle。

| 目标 | 产物 | 说明 |
|---|---|---|
| `make build` | 下面各项 | `webui niq`；装了 Rust + Tauri CLI 时还会构建 `desktop` |
| `make webui` | `app/client/webui/assets/dist` | 先 `npm ci`，再 `tsc -b && vite build`；产物提交进仓库 |
| `make niq` | `./bin/niq` | 注入 `-X main.version`（来自 `git describe`）与 `main.buildTime` |
| `make desktop` | `app/client/desktop/src-tauri/target/release/bundle/` 下的安装包 | 可安装的套壳：内置 `./bin/niq`，打开网关自带的 WebUI；需要 Go + Rust + Tauri CLI |
| `make dist-desktop` | `dist/niq-desktop_<version>_<arch>.{dmg,app.zip}` | 发布产物；`UNIVERSAL=1` 构建双架构（`app/publish/desktop/release.sh`） |
| `make desktop-sidecar` | `app/client/desktop/src-tauri/binaries/niq-<triple>` | 只把 CLI 放到位，不构建安装包 |
| `make install` | `$(PREFIX)/niq` 软链 + `$(APP_DIR)` 里的 `niq.app`（默认 `/Applications`） | 链接 CLI、安装补全，并把已构建的 App 复制过去（免 sudo 用 `APP_DIR=$HOME/Applications`） |
| `make snapshot` | `dist/` | 完整的发布试跑，不上传（见 §7） |
| `make npm-publish` | npm 包 | `DRY=--dry-run` 预览；需要已构建的 `dist/` |

`make install` 从不构建：`./bin/niq` 不存在时它会直接说明并停下，所以安装不会悄悄变成一次
漫长的 npm/vite 构建。

## 4. Go：build、vet、test

```sh
go build ./...
go vet ./...
go test ./...            # 笔记本上约 6 秒
```

测试是自封闭的：任何需要数据根的测试都把 `NIQ_HOME` 指向 `t.TempDir()`，所以跑测试既不会读
也不会写你真实的 `~/.niq`。服务端测试起的是**临时端口上的真实 listener**（`127.0.0.1:0`），
而不是 mock 掉 HTTP 层 —— 网关、项目 API、总线传输、worker supervisor 都是按它们真实的
进程/链路形态被验证的。

常用形式：

```sh
go test ./app/standalone/gateway -count=1 -v          # 单个包，不吃缓存
go test ./app/standalone/project -run TestRunProject  # 单个测试
go test ./core/impl/eventbus/ -count=1                # 总线引擎
go test -race ./app/standalone/...                    # 并发敏感的包
```

如果你刚改了磁盘上的 fixture，`-count=1` 很重要：Go 是按包缓存结果的。

测试大致按区域分布：

| 区域 | 有测试的包 |
|---|---|
| 入口与实例 | `app/standalone/cli`、`launcher`、`project`、`project/api`、`gateway` |
| worker 控制面 | `app/standalone/worker`、`worker/builtin`（含 `worker/builtin/workers`）、`worker/process` |
| 存储与配置 | `app/standalone/store`、`store/migrate`、`template` |
| 机制 | `core/impl/eventbus`（含 `transport/httptrans`）、`workerctl`、`busctl`、`iface`、`embedws` |
| worker | `core/impl/reason`、`core/impl/workers/*` |
| provider | `core/impl/provider/{openai,openairesponses,anthropic}` |

### 端到端测试套件

上面都是在包内部测。`e2e/suites/` 不同：它编译出 CLI，然后像用户一样驱动它——argv 进去，退出码与输出出来，状态写到磁盘，HTTP 打在真实端口上。它不 import 被测包，所以行为变了才会红，签名变了不会。

它们由 `e2e` 构建标签保护，所以 `go test ./...` 依然很快：

```sh
make e2e                       # 全部套件
make e2e-cli                   # 装配层：启停/重启、端口、输出、补全
make e2e-bus                   # 数据面：dial-in 参与者、投递、ACL、请求-回复
go test -tags e2e ./e2e/suites/cli -run TestE2EStop -v
```

三套件，按观察的层划分：

| 套件 | 驱动什么 | 断言什么 |
|---|---|---|
| `cli` | 像用户一样驱动二进制 | 退出码、输出、`shell.json`、端口 |
| `bus` | 以 dial-in 身份接真实 HTTP 传输 | 定向投递、广播、活动连接上的 ACL 变更、请求-回复、transient 与持久历史 |
| `worker` | 内置 worker 类型 | timer 触发、workspace 挂载、directory 名册、program 存储、host 生命周期、一轮桩模型推理 |

每条测试有自己的 `NIQ_HOME`、自己的 `HOME`，以及一个 `open`/`npm` 都空转的 `PATH`；网关一律起在 `127.0.0.1:0`——所以这套东西可以在已经跑着 niq 的机器上并行跑。每个沙箱在测试结束前会停掉自己启动的全部进程，包括 `niq i`／`niq w` 自己拉起的网关。

## 5. WebUI 开发

bundle 是 `app/client/webui/assets` 里的一个 Vite 应用。

```sh
make build webui     # 一次性生产构建（tsc -b && vite build）
```

需要反复迭代时，在网关已运行的前提下起开发服务器：

```sh
cd app/client/webui/assets
npm run dev          # vite，:5173
```

开发服务器会把 API 流量代理到 `:9527` 的网关，因此两个页面都能带热更新工作：

- 网关自己的界面：http://localhost:5173/
- 某个项目的界面：http://localhost:5173/p/&lt;id&gt;/（该项目必须在运行）

网关重启后请刷新开发页面；另外记住**项目**页面加载的是服务它的那个进程的 bundle —— 所以
旧实例会一直显示旧 UI，直到你重启该实例（`niq restart -p`）。

## 6. TypeScript SDK

进程外 SDK 在 `sdk/ts`，有自己的一套测试（Vitest）：

```sh
cd sdk/ts
npm ci
npm test             # vitest run
npm run typecheck    # tsc --noEmit
npm run build        # tsc -p tsconfig.build.json → dist/（不提交）
```

`npm run release` 会依次跑 test、typecheck、build，工作区不干净时拒绝执行，然后发布 —— 它是
SDK 自己的发版路径，不属于 CLI 的发布流程。

## 7. 本地试跑发布流水线

不上传、不发布：

```sh
make snapshot        # goreleaser release --snapshot --clean，然后 npm publish --dry-run
```

它会把 linux/darwin/windows（amd64 与 arm64）的 CLI 归档构建到 `dist/`，并组装 npm 目录树，
所以你可以直接检视一次发布究竟会发什么。真正的发布（打 tag、GitHub Release、mac job）
在 `.github/workflows/release.yml`；`make npm-publish` 从已有的 `dist/` 发布 npm 包
（`DRY=--dry-run` 预览）。

## 8. 现在覆盖了什么

- **Go 单元/集成测试：109 个 `_test.go`、727 个测试**，约 6 秒全绿（加 `-race` 约 18 秒）。
  覆盖网关的 HTTP 契约、launcher 的进程协议、总线引擎与传输、worker 生命周期与
  placement、存储与迁移、provider 适配器，以及 worker 类型库。
- **黑盒套件：22 个 `_test.go`、58 个用例**，位于 `e2e/suites/{cli,bus,worker}`，约 8 秒全绿。
  进程边界（二进制 → `niq` → 网关 → `/p/{id}/` → 项目进程）只有它们覆盖（§4）。
- **TypeScript SDK：8 个 Vitest 文件、64 个测试**（`sdk/ts`）。

覆盖跟着代码走：加一个命令就给 `e2e/suites/cli` 加一个用例，加一种 worker 类型就给
`e2e/suites/worker` 加一个。

## 9. CI

`.github/workflows/ci.yml` 在每次推送到 `dev`/`main` 和每个 pull request 上运行。它不发布
任何东西——那是 `release.yml` 的事——各 job 并行：

| job | runner | 证明什么 |
|---|---|---|
| `go` | ubuntu、macOS | `go build`、`go vet`、`go test -race ./... -count=1` |
| `e2e` | ubuntu、macOS | `go vet -tags e2e ./e2e/...`，然后 `make e2e` |
| `cross` | ubuntu | `GOOS=windows`（amd64、arm64）、`linux/arm64`、`darwin/amd64` 仍能编译 |
| `sdk` | ubuntu | `sdk/ts` 的 `npm ci`、`npm test`、`typecheck`、`build` |
| `webui` | ubuntu | 提交进仓库的 bundle 就是 `vite build` 的产物（§2） |
| `desktop` | macOS | `app/publish/desktop/release.sh --universal` 能构建、签名并通过校验；两个可执行文件都是 x86_64 + arm64 |

为什么这么选：

- **`-race` 是 job 的一部分，不是附加项。** 总线是并发事件系统，那里的竞争不是让测试
  失败，而是让进程死掉；不带检测器跑绿，说服力比看上去小。被取代的推送会取消正在跑的
  那一次（`concurrency`），这是它跑得起的前提。
- **两个平台，不只 Linux。** 有差异的恰恰是这套东西的重点：进程信号、端口占用者、
  loopback 绑定、`open` 与 `xdg-open`。只在 Linux 上跑，`helper` 的 macOS 那一半就是
  没被测过的。
- **`cross` 存在是因为发布要出六个归档。** 某个 `_windows.go` 停止编译，在上面那些
  runner 上是看不见的，只会在打 tag 时爆出来。
- **bundle 检查是在落实 §2。** 嵌入的 bundle 过期时 `go build ./...` 依然通过，所以
  "把重建的 `dist` 一起提交"这条规则需要一个在忘记时失败的东西。
- **`desktop` 是任何 Go 测试都代替不了的那一个产物。** 桌面壳是一个 Rust crate，外面包着
  Go sidecar，由 shell 脚本组装；这些在打 tag 之前哪儿都没跑过，所以 v0.18.0 发出来时
  根本没有 macOS 包。它按路径门控（`app/client/desktop`、`app/publish/desktop`、
  `Makefile`），因为 universal 的 Tauri 构建要几分钟；它跑的是发布脚本而不是直接
  `tauri build`，被测的就是分发路径本身。

每个 job 都能在本地复现：直接跑表格里那一行的命令即可，都不需要 secret、service container
或特别准备的环境。
