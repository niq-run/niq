# niq

[English](../README.md) · **简体中文**

niq 是一个**事件驱动的智能体运行时**。它是一个由许多 **Worker** 组成的单一智能体，这些 Worker 通过事件总线协作 —— 每个 Worker 只负责自己的关注点，合起来构成完整的智能体。

niq 恰好有三个核心概念：

- **Worker** —— 能做事的工作单元，像一个 **actor**：它有自己的状态，只通过发送与响应消息来通信。Worker 订阅事件、处理事件、发布新事件。Worker 是 niq 唯一的扩展点。
- **Program** —— Worker 能力的源码：自然语言提示词（类似主流 agent 的 skills），或 DSL 脚本（类似程序化工具调用，PTC）。两种形式都是用来生成工具调用的源码。
- **Event** —— Worker 之间唯一的通信语言，经事件总线投递。

> **协作即扩展。**

niq 的成长不是靠增加新的抽象，而是靠增加更多在事件总线上协作的参与者。每一项能力（推理、工具执行、安全守卫、生命周期）都是一个 Worker。扩展一项能力，和让一个人加入协作，是同一件事 —— 用户只是另一个协作参与者，可以有一个，也可以有很多个。在 niq 里，一切都是 Worker 在事件总线上协作。

> **状态：早期、快速演进中。** 设计仍在变化，API 与行为可能随时调整，恕不另行通知。

## 快速开始

`niq` 只有一个入口：装上它，然后运行它。

```sh
npm install -g @niq.run/niq   # 也可以用安装脚本，或从源码构建
niq                           # 在后台启动网关并汇报状态
```

它会打印网关的地址、日志位置，以及每个项目的端口 —— 然后让开。之后
**http://localhost:9527** 就是 WebUI，`niq i` 是终端里的同一个东西。

安装（三种方式）、升级、每条命令与每个参数、服务端部署、环境变量，全部在
**[doc/install-and-cli.zh-CN.md](install-and-cli.zh-CN.md)**（[English](install-and-cli.md)）。

## 模型提供方

`reason` worker 与 LLM 提供方通信。提供方设置（端点、模型、API Key）存放在 `~/.niq/common/providers/provider.json`；`api_key` 和各个 header 的值支持环境变量展开 —— 例如 `"api_key": "${OPENAI_API_KEY}"` 可以让密钥不落到存储文件里。

## 项目结构

`core/` 放不依赖部署形态的契约与机制；`app/` 放各形态与产品入口，其中 `app/client/` 是界面，
`app/publish/` 是分发。

[doc/directory-layout.zh-CN.md](directory-layout.zh-CN.md) 是完整版本：as-built 源码树、
背后的归属规则、新东西放哪里，以及强制这些规则的依赖校验（[English](directory-layout.md)）。

## 构建与测试

只装 Go 就能构建、vet 与测试后端 —— WebUI bundle 是提交进仓库并用 `go:embed` 嵌入的，
所以编译 CLI 不需要 Node：

```sh
go build ./... && go vet ./...
go test ./...            # 约 6 秒
make build               # ./bin/niq，macOS 上还有 app
```

[doc/build-and-test.zh-CN.md](build-and-test.zh-CN.md) 里有 make 目标、怎么只跑一个包或
一个测试、WebUI 开发服务器、TypeScript SDK、本地发布试跑，以及现在覆盖了什么、还缺什么
（[English](build-and-test.md)）。

## 数据目录

niq 写的一切都在 `~/.niq` 之下（`NIQ_HOME` 可迁移根目录；`NIQ_AUTH_CONFIG` 与
`NIQ_PROVIDER_CONFIG` 可整体覆盖那两个文件）。目录树、每个文件归谁所有、每个进程读什么，
见 [doc/install-and-cli.zh-CN.md §6](install-and-cli.zh-CN.md#6-它往哪里写)
（[English](install-and-cli.md#6-where-it-writes)）。
