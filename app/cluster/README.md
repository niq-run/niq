# app/cluster

Placeholder for the **cluster deployment variant** of niq. There is no
implementation here yet.

This is where the distributed form will live: a deployable multi-node
implementation, shipped with a Helm chart, including

- a distributed event bus,
- the worker control-plane services that place and manage workers across nodes
  and containers,
- the storage adapters that keep manifests, runtime state and bus identities on
  shared storage.

`app/cluster` sits beside `app/standalone`, the single-machine variant. The two
are separate builds: neither imports the other, and both are built on the
contracts under `core/itfs`.
