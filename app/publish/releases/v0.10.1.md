# niq v0.10.1

A follow-up to 0.10.0 that tightens how capabilities are invoked — and, just as
importantly, what happens when a call cannot be delivered. Nothing changes for
workers that only use their own advertised tools.

## Highlights

- **`request` is a generalized call.** It is no longer checked against what this
  worker happened to discover: the model learns `(target, event, arguments)` from
  the directory worker's `get_worker_info` and the event goes straight out.
- **`Internal` is self-scoped** (renamed from `SelfOnly`). An internal capability
  is not advertised to peers, and only its owner may invoke it.
- **No more silent request failures.** A request that the bus refuses, cannot
  route, or cannot deliver — or that reaches a worker with no handler — is now
  answered with the original request id instead of being dropped.
- **`@niq.run/worker-sdk` 0.7.0** is published with the matching behaviour.

## 1. `request` is a generalized call

Previously `request` resolved `(target, event)` against the worker's discovered
universe and failed with "unknown capability" otherwise. That contradicted its
purpose: it is the escape hatch for a capability the model learns about through
`get_worker_info`, not one that was announced to this worker.

It now validates only that `target` and `event` are non-empty and sends the event
directly. Discovery decides what is *listed as a tool*, not what may be called.

This is what makes another reason worker's `worker.readme`, `provider.switch` or
`program.query` reachable: those events are never surfaced as tools to a peer
reason worker, but they are visible in the roster and callable through `request`.

## 2. `Internal` is self-scoped (renamed from `SelfOnly`)

The flag previously only hid an extension from the peer-facing catalogue;
dispatch was ungated, so "self only" did not actually restrict anything.

- **Not advertised**: still left out of `worker.ready`'s peer-facing `watch`, so
  the directory roster, the WebUI event picker and PTC scripts never see it.
- **Only its owner may invoke it**: `dispatchExtension` refuses a request from
  any other worker before the handler runs and answers with `request.rejected`.
  The bus stamps the sender's worker id from its credential.
- The declaring worker still lists it in its own tool list.

This is a scope rule, not access control: whether a peer may address the worker
at all is still the bus ACL's job.

`message` and `request` stay internal (they are that worker's own escape
hatches). `worker.readme.update` is now advertised, so the WebUI and authorized
peers can discover it; the ACL decides who may send it.

## 3. No more silent request failures

| Where | Failure | Reply |
|---|---|---|
| bus | publish ACL denied | `request.rejected` — `publish denied: …` |
| bus | target offline | `request.failed` — `target is not online: …` |
| bus | delivery error | `request.failed` — `delivery to … failed: …` |
| worker | no handler for the event | `request.failed` — `no handler for event …` |

Replies carry the original request id and trace id, and are persisted so they
show up in the event log and the WebUI. Only requests get a reply; notifications
and broadcasts stay silent, since nothing is waiting on them.

## TypeScript SDK

`@niq.run/worker-sdk` **0.7.0** is published and mirrors the worker-side changes:
`Extension.selfOnly` is now `Extension.internal`, `dispatchExtension` refuses a
foreign sender, and an unmatched request is answered with `request.failed`.

## Upgrade notes

- **TypeScript workers**: the rename is breaking — upgrade to 0.7.0 and replace
  `selfOnly` with `internal` (0.x caret will not pick 0.7.0 up automatically).
  Workers that never set the flag need no changes.
- Any worker that relied on invoking another worker's `message`/`request` should
  stop: those are self-scoped now and answered with `request.rejected`.
- Nothing changes for workers that only call advertised capabilities.

## Validation

- `go build ./...`, `go vet ./core/... ./app/...`, `go test ./core/... ./app/...` clean.
- `npm test` in `sdk/ts` — 50 tests pass.
- End-to-end against a real project: a foreign `message` is rejected, an
  advertised `worker.readme.update` completes, an unknown event returns
  `no handler for event …`, and `request` reaches a peer reason's `worker.readme`.
- Release binaries for darwin / linux / windows on amd64 + arm64, with
  `niq --version` reporting `0.10.1`.
