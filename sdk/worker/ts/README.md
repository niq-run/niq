# @niq-ai/niq-worker

TypeScript SDK for authoring **niq Workers** that connect to a niq event bus
over HTTP (SSE + POST).

> **This is one implementation of the niq bus protocol.** The transport is the
> contract — you can connect a Worker written in any language that speaks HTTP
> + SSE. This package is just the TS convenience layer.

## What it is / is not

| Included | Not included |
|---|---|
| Connect to a bus over HTTP (`GET /events` SSE stream) | Identity registration (control-plane / `registryapi`) |
| `send` (directed) and `broadcast` (subscription-matched) | Worker runtime / swarm assembly |
| Receive routed events (`tool.request`, etc.) | Program compilation |
| Protocol event type constants | — |
| Event construction helpers (`createEvent`, correctly timestamped + UUIDv7 ids) | — |

Identity must already be registered on the bus (offline, by the control plane)
before a worker connects. This SDK only establishes the connection.

## Usage

```ts
import { HTTPWorkerClient, createEvent, EventType } from "@niq-ai/niq-worker";

const worker = new HTTPWorkerClient({
  baseURL: "http://localhost:8080",
  workerID: "my-worker@me",
  credential: "the-credential-issued-at-registration",
});

await worker.connect();

// Handle events routed to this worker.
for await (const evt of worker.events()) {
  // respond to tool.request, worker.input, ...
}

// Directed delivery — "I know who should handle this."
await worker.send(
  createEvent(EventType.ToolCompleted, { result: "ok" }),
  "reason.0",
);

// Broadcast — "whoever subscribes handles it."
await worker.broadcast(createEvent("stats.heartbeat", { uptime: 42 }));

await worker.close();
```

## Protocol notes

- **Every request carries its own `worker_id` + `credential`** — no session
  token. The SSE stream is the connection anchor: the bus requires it open
  before it accepts publishes, and keeps one live connection per worker.
- **You must supply `id` and `timestamp`** — the bus does not fill them.
  Use `createEvent` (UUIDv7 id + unix-seconds timestamp + `specversion`).
- **Do not set `worker_id` yourself** — the bus overwrites it on receipt
  (anti-spoofing, design D8). The client stamps it at publish time.
- `send` requires at least one target; use `broadcast` for undirected delivery.
- If no one subscribes, a broadcast silently disappears — that is a valid end
  of the execution path, not an error.

## Development

```sh
npm install
npm run typecheck
npm test       # vitest
npm run build  # tsc → dist/
```

## Status

Early, moving fast. Pre-1.0; the wire protocol is stabilizing alongside
`core/bus` and the Go `httptrans` transport.

`node >= 18` (uses global `fetch` and `ReadableStream`).