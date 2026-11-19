# app/cluster（占位）

`app/cluster` 是 **分布式部署形态** 的顶层命名空间，与 `app/standalone`（本地单机形态）平级、互不 import。

当前为空占位。未来它将包含：

- **另一套 worker 控制面**：把 worker 调度到其它节点/容器，而不是本地进程或子进程。
- **另一套 placement 实现**：`in-process` 之外，本地子进程、远程容器、跨节点租约都各自成立；placement 的语义由 `core/itfs/worker.Placement` 固定，具体执行器属于本形态。
- **分布式存储 provider**：实现 `worker.ManifestStore`、`workerlife.WorkerStore`、`bus.Store`，把清单、运行时快照、总线身份放到共享存储。

## 共享边界

cluster 不复用 `app/standalone` 的任何实现。两者唯一的共享面是 `core/itfs` 契约：

- `worker.Backend`：host worker 依赖的完整控制面能力（`Lifecycle` + `Manifest`）。host 在两种形态之间零改动。
- `bus.Control` / `bus.Store`：总线身份控制面契约。
- `workerlife.WorkerStore`：运行时状态存储契约。

接入时需要实现这些接口并注入，再按 `app/standalone/worker.ControlPlane` 同等的职责组装本形态的控制面；不要反向依赖 `app/standalone/*`。

## 校验

本目录不得出现在 `app/standalone/**` 的 import 图里，反之亦然。可以用：

```sh
go list -deps ./app/standalone/... | grep app/cluster   # 应为空
go list -deps ./app/cluster/...    | grep app/standalone # 应为空
```
