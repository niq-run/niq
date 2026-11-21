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
make build              # 本机能构建的全部：webui、niq、（macOS 上还有 miq）
make build webui        # 只构建 bundle
make build niq          # 只构建 CLI，按现状嵌入 bundle
make build niq webui    # 先 bundle 再 CLI —— 词序不影响结果
make install            # 把已构建的产物软链到 $(PREFIX)；从不构建
```

`build` 后面的词是**目标，不是参数** —— make 会把它们都跑一遍 —— 所以 `make niq`、
`make webui`、`make miq` 单独用也可以，而只有 `make build` 时仍然表示“全部”。当 `niq` 与
`webui` 一起写时，无论顺序如何都会先构建 bundle。

| 目标 | 产物 | 说明 |
|---|---|---|
| `make build` | 下面各项 | Linux 上是 `webui niq`，macOS 上是 `webui niq miq` |
| `make webui` | `app/client/webui/assets/dist` | 先 `npm ci`，再 `tsc -b && vite build`；产物提交进仓库 |
| `make niq` | `./bin/niq` | 注入 `-X main.version`（来自 `git describe`）与 `main.buildTime` |
| `make miq` | `dist/niq.app`、`./bin/miq` | 仅 macOS；打包逻辑在 `app/publish/mac/build.sh` |
| `make miq-universal` | `dist/niq.app`（arm64 + x86_64） | 仅 macOS |
| `make dist-miq` | `dist/niq_mac_darwin_universal.zip` | 发布时要发的那个包 |
| `make install` | `$(PREFIX)` 下的软链（默认 `~/.local/bin`） | 链接 `./bin/niq`（构建过 `miq` 也一起链），随后安装补全 |
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
| worker 控制面 | `app/standalone/worker`、`worker/process` |
| 存储与配置 | `app/standalone/store`、`store/migrate`、`template` |
| 机制 | `core/impl/eventbus`（含 `transport/httptrans`）、`workerctl`、`busctl`、`iface`、`embedws` |
| worker | `core/impl/reason`、`core/impl/workers/*` |
| provider | `core/impl/provider/{openai,openairesponses,anthropic}` |

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

## 8. 现在覆盖了什么，什么还在长

- **Go 单元/集成测试：105 个 `_test.go`**，全部通过约 6 秒。覆盖网关的 HTTP 契约、
  launcher 的进程协议、总线引擎与传输、worker 生命周期与 placement、存储与迁移、
  provider 适配器，以及 worker 类型库。
- **TypeScript SDK：8 个 Vitest 文件**（`sdk/ts`）。
- **还没有 CI 测试任务**：唯一的 workflow 是 `release.yml`，负责构建与发布。测试目前在本地跑。
- **还没有端到端测试**。进程边界（二进制 → `niq` → 网关 → `/p/{id}/` → 项目进程）目前只由
  在同进程内起真实 listener 的单元测试覆盖。

这两块都在持续完善：单元覆盖会跟着新代码走，针对真实多进程路径的 e2e 套件也在计划中。
等它落地后，本节会补上怎么跑（make 目标、产物、会占用哪些端口），而不是现在就凭空描述。
