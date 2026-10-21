# @niq.run/worker-sdk

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
| Receive routed events (`request.completed`, etc.) | Program compilation |
| Base worker (`BaseWorker`: extension registry, tool replies, presence announcement) | — |
| Protocol event type constants | — |
| Event construction helpers (`createEvent`, correctly timestamped + UUIDv7 ids) | — |

Identity must already be registered on the bus (offline, by the control plane)
before a worker connects. This SDK only establishes the connection.

## Usage

```ts
import { HTTPWorkerClient, createEvent, EventType } from "@niq.run/worker-sdk";

const worker = new HTTPWorkerClient({
  baseURL: "http://localhost:8080",
  workerID: "my-worker@me",
  credential: "the-credential-issued-at-registration",
});

await worker.connect();

// Handle events routed to this worker.
for await (const evt of worker.events()) {
  // respond to request.completed, worker.input, ...
}

// Directed delivery — "I know who should handle this."
await worker.send(
  createEvent(EventType.RequestCompleted, { result: "ok" }),
  "reason.0",
);

// Broadcast — "whoever subscribes handles it."
await worker.broadcast(createEvent("stats.heartbeat", { uptime: 42 }));

await worker.close();
```

```ts
import { HTTPWorkerClient, readBusEnv } from "@niq.run/worker-sdk";

// Launched by the niq supervisor? Connect straight from the environment.
const client = new HTTPWorkerClient(readBusEnv());
```

### Declaring a tool's schemas

`parameters` (the request) and `response` (the reply) are JSON Schema on the
wire. Write them with the `s` builders instead of by hand:

```ts
import { s } from "@niq.run/worker-sdk";

worker.register(
  {
    event: "lark.send",
    description: "Send a message to a chat",
    parameters: s.object({
      target: s.string("chat_id, open_id or user_id"),
      text: s.string("message body"),
      file: s.optional(s.string("local path or http(s) URL")),
      mode: s.optional(s.enumOf(["fast", "deep"])),
    }),
    response: s.object({ messageId: s.string(), ok: s.boolean() }),
  },
  handler,
);
```

Properties are required unless wrapped in `s.optional(...)`; arrays, enums and
free-form values are `s.array(...)`, `s.enumOf([...])`, `s.any()`.

Both schemas ride `worker.ready`, so peers and the directory worker see them
(`get_worker_info` → `parameters` / `response`). `response` is what lets an agent
— in particular a PTC script, written before anything runs — plan how to parse a
reply; declare it whenever the reply is structured.

Already using zod (or valibot/arktype)? Skip the builders: `parameters` and
`response` take any JSON Schema object, and those libraries generate one
(`z.toJSONSchema(schema, { io: "input" })` in zod 4). Validate the args inside
the handler with the same schema — nothing on the bus validates for you.

> TypeScript types cannot be reflected at runtime, so there is no "infer the
> schema from the handler signature" option: a build step would be needed
> (typedoc/ts-json-schema-generator/typia). Explicit schemas — or a zod
> one-liner — are the practical route.

### Reply shape

`request.completed` payloads follow one rule (the same as the Go `baseworker`):

- an **object** reply *is* the payload — no wrapper;
- anything else (text, a number, an array) is wrapped as `{ result: value }`.

So a tool that returns structured data just passes the object:
`replyCompleted(caller, callID, { mounts, primary })`; text goes through
unchanged: `replyCompleted(caller, callID, "Exit code: 0")`. `replyObject(...)`
and `replyString(...)` are the same calls with the shape made explicit.

`result` and `content` are reserved top-level keys (the latter for multi-modal
blocks), so a structured reply should pick its own field names.

Reading a peer's reply uses the mirror helpers:

```ts
import { replyValue, replyText } from "@niq.run/worker-sdk";

const listing = replyValue(evt.payload); // the object itself, or the result value
const line = replyText(evt.payload);     // string verbatim, else JSON
```

### Base worker

`BaseWorker` (mirrors Go `pkg/baseworker`) layers the shared worker plumbing
on top of a `WorkerSideChannel`: an extension registry (what the worker
responds to and how), tool-request reply plumbing, and the `worker.ready`
presence announcement.

```ts
import { BaseWorker, HTTPWorkerClient, parseToolCall, EventType } from "@niq.run/worker-sdk";

const worker = new BaseWorker({
  id: "echo@me",
  subscriptions: [{ type: "tool.invoke" }],
  channel: new HTTPWorkerClient({
    baseURL: "http://localhost:8080",
    workerID: "echo@me",
    credential: "...",
  }),
});

// Declare what this worker responds to. Safe at any time — before or after
// connecting. Same (event, keyField, key) re-registration replaces the handler.
worker.register(
  { event: "tool.invoke", keyField: "name", key: "echo", description: "Echo a message" },
  (evt) => {
    const call = parseToolCall(evt);
    worker.replyCompleted(call.callerID, call.callID, `echo: ${call.args["text"] ?? ""}`, call.traceID);
  },
);

await worker.channel.connect();
await worker.announceReady("echo"); // broadcast worker.ready, self excluded

for await (const evt of worker.channel.events()) {
  if (!worker.dispatchExtension(evt)) {
    // no handler matched — e.g. replyUnknownTool(parseToolCall(evt))
  }
}
```

### CLI helpers (writing a worker `start` entrypoint)

Out-of-process workers ship a `src/start.ts` (compiled to the package `bin`) to
run the worker as its own process. `cli` provides the shared launch glue:

```ts
import { busConnFromArgs, parseCliArgs, runWorkerApp, HTTPWorkerClient } from "@niq.run/worker-sdk";

const { help, opts } = parseCliArgs(process.argv);
if (help) { printUsage(); process.exit(0); }

await runWorkerApp(async () => {
  const client = new HTTPWorkerClient(busConnFromArgs(opts)); // CLI flags layered over NIQ_BUS_* env
  await client.connect();
  // ... run your worker ...
});
```

- `parseCliArgs(argv)` — parse `--key value` / `--key`, flag `--help` / `-h`.
- `busConnFromArgs(opts, env?)` — merge bus connection params: explicit
  `--bus-url` / `--worker-id` / `--credential` flags override the
  supervisor-injected env (`NIQ_BUS_URL` / `NIQ_WORKER_ID` / `NIQ_WORKER_CREDENTIAL`).
- `runWorkerApp(main)` — run the app body, log + set `process.exitCode = 1` on failure.

## Protocol notes

- **Every request carries its own `worker_id` + `credential`** — no session
  token. The SSE stream is the connection anchor: the bus requires it open
  before it accepts publishes, and keeps one live connection per worker.
- **You must supply `id` and `timestamp`** — the bus does not fill them.
  Use `createEvent` (UUIDv7 id + unix-seconds timestamp + `specversion`).
- **Do not set `worker_id` yourself** — the bus overwrites it on receipt
  (anti-spoofing, design D8). The client stamps it at publish time.
- `send` requires at least one target; use `broadcast` for undirected delivery.
- **Environment bootstrap**: when the niq project supervisor launches an
  external worker, it injects `NIQ_BUS_URL`, `NIQ_WORKER_ID` and
  `NIQ_WORKER_CREDENTIAL`. Use `readBusEnv()` to read them (throws, naming the
  missing variables, when absent) and construct the client from the result:
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