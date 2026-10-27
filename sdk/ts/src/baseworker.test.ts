import { describe, expect, it } from "vitest";
import { BaseWorker, argInt, argString, parseToolCall } from "./baseworker.js";
import type { Event, WorkerSideChannel } from "./types/index.js";
import { EventType } from "./types/index.js";

class FakeChannel implements WorkerSideChannel {
  readonly id = "fake";
  sent: Array<{ evt: Event; targets: string[] }> = [];
  broadcasts: Event[] = [];

  async connect(): Promise<void> {}
  async send(evt: Event, ...targets: string[]): Promise<void> {
    this.sent.push({ evt, targets });
  }
  async broadcast(evt: Event): Promise<void> {
    this.broadcasts.push(evt);
  }
  // eslint-disable-next-line require-yield
  async *events(): AsyncIterable<Event> {}
  async close(): Promise<void> {}
}

function makeEvent(overrides: Partial<Event>): Event {
  return {
    id: "evt-1",
    type: "tool.invoke",
    status: "created",
    payload: {},
    worker_id: "caller.0",
    timestamp: 1_700_000_000,
    ...overrides,
  };
}

function makeWorker(channel: FakeChannel): BaseWorker {
  return new BaseWorker({
    id: "echo.0",
    subscriptions: [{ type: "tool.invoke" }],
    channel,
  });
}

describe("extension registry", () => {
  it("dispatches by event type", async () => {
    const w = makeWorker(new FakeChannel());
    const seen: Event[] = [];
    w.register({ event: "tool.invoke" }, (evt) => {
      seen.push(evt);
    });

    const evt = makeEvent({});
    expect(w.dispatchExtension(evt)).toBe(true);
    expect(seen).toEqual([evt]);
  });

  it("replaces same-event registrations", () => {
    const w = makeWorker(new FakeChannel());
    const hits: string[] = [];
    w.register({ event: "tool.upper" }, () => {
      hits.push("upper");
    });
    w.register({ event: "tool.lower" }, () => {
      hits.push("lower");
    });
    // Re-registering the same event replaces it.
    w.register({ event: "tool.upper" }, () => {
      hits.push("upper-2");
    });

    expect(w.dispatchExtension(makeEvent({ type: "tool.lower" }))).toBe(true);
    expect(w.dispatchExtension(makeEvent({ type: "tool.upper" }))).toBe(true);
    // An event no worker handles does not match.
    expect(w.dispatchExtension(makeEvent({ type: "tool.other" }))).toBe(false);
    expect(hits).toEqual(["lower", "upper-2"]);
  });

  it("returns false when nothing matches", () => {
    const w = makeWorker(new FakeChannel());
    expect(w.dispatchExtension(makeEvent({ type: "other.event" }))).toBe(false);
  });

  it("refuses an internal extension from a foreign sender", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    const seen: Event[] = [];
    w.register({ event: "internal.op", internal: true }, (evt) => {
      seen.push(evt);
    });

    const peer = makeEvent({ type: "internal.op", worker_id: "peer.0", request_id: "r1" });
    expect(w.dispatchExtension(peer)).toBe(true);
    await new Promise((r) => setImmediate(r));
    expect(seen).toEqual([]);
    expect(ch.sent.map((s) => s.evt.type)).toEqual([EventType.RequestRejected]);

    // The owner invokes it normally.
    const mine = makeEvent({ type: "internal.op", worker_id: "echo.0", request_id: "r2" });
    expect(w.dispatchExtension(mine)).toBe(true);
    expect(seen).toEqual([mine]);
  });

  it("answers an unhandled request with request.failed", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);

    const evt = makeEvent({ type: "nope", worker_id: "caller.0", request_id: "r1" });
    expect(w.dispatchExtension(evt)).toBe(false);
    await new Promise((r) => setImmediate(r));
    expect(ch.sent.map((s) => s.evt.type)).toEqual([EventType.RequestFailed]);
    expect(ch.sent[0].evt.request_id).toBe("r1");
    expect(ch.sent[0].evt.payload["error"]).toBe("no handler for event nope");

    // A notification is dropped silently: nothing is waiting on it.
    w.dispatchExtension(makeEvent({ type: "nope", worker_id: "caller.0" }));
    await new Promise((r) => setImmediate(r));
    expect(ch.sent).toHaveLength(1);
  });

  it("renders watch entries, omitting internal for peers", () => {
    const w = makeWorker(new FakeChannel());
    w.register(
      {
        event: "tool.invoke",
        description: "Echo a message",
        parameters: {
          type: "object",
          properties: { prefix: { type: "string" } },
        },
      },
      () => {},
    );
    w.register(
      { event: "self.list_workers", internal: true, description: "my view" },
      () => {},
    );

    expect(w.watchEntries(false)).toEqual([
      {
        event: "tool.invoke",
        desc: "Echo a message",
        parameters: {
          type: "object",
          properties: { prefix: { type: "string" } },
        },
      },
    ]);
    expect(w.watchEntries(true)).toHaveLength(2);
  });
});

describe("tool call parsing", () => {
  it("extracts common fields and defaults args to an empty object", () => {
    const evt = makeEvent({
      request_id: "req-7",
      trace_id: "trace-1",
      type: "echo",
      payload: { text: "hi" },
    });
    expect(parseToolCall(evt)).toEqual({
      callID: "req-7",
      name: "echo",
      callerID: "caller.0",
      args: { text: "hi" },
      traceID: "trace-1",
    });

    const bare = parseToolCall(makeEvent({ type: "echo", payload: {} }));
    expect(bare.callID).toBe("");
    expect(bare.name).toBe("echo");
    expect(bare.args).toEqual({});
  });

  it("reads scalar args with defaults", () => {
    const args = { s: "x", n: 2.9, gone: undefined } as Record<
      string,
      unknown
    >;
    expect(argString(args, "s")).toBe("x");
    expect(argString(args, "missing")).toBe("");
    expect(argInt(args, "n", 0)).toBe(2);
    expect(argInt(args, "gone", 5)).toBe(5);
  });
});

describe("replies", () => {
  it("replyCompleted sends request.completed echoing the request id", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    await w.replyCompleted("caller.0", "req-7", "ok", "trace-1");

    expect(ch.sent).toHaveLength(1);
    const { evt, targets } = ch.sent[0];
    expect(targets).toEqual(["caller.0"]);
    expect(evt.type).toBe(EventType.RequestCompleted);
    expect(evt.request_id).toBe("req-7");
    expect(evt.trace_id).toBe("trace-1");
    expect(evt.payload).toEqual({ result: "ok" });
  });

  it("an object reply IS the payload; other values wrap as {result}", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    await w.replyCompleted("caller.0", "r1", { mounts: ["/a"], primary: "/a" });
    await w.replyCompleted("caller.0", "r2", ["a", "b"]);
    await w.replyCompleted("caller.0", "r3", 42);
    await w.replyObject("caller.0", "r4", { content: [{ type: "image" }] });
    await w.replyString("caller.0", "r5", "Exit code: 0");

    expect(ch.sent[0].evt.payload).toEqual({ mounts: ["/a"], primary: "/a" });
    expect(ch.sent[1].evt.payload).toEqual({ result: ["a", "b"] });
    expect(ch.sent[2].evt.payload).toEqual({ result: 42 });
    expect(ch.sent[3].evt.payload).toEqual({ content: [{ type: "image" }] });
    expect(ch.sent[4].evt.payload).toEqual({ result: "Exit code: 0" });
  });

  it("replyFailed and replyRejected carry error / reason", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    await w.replyFailed("caller.0", "r", "boom");
    await w.replyRejected("caller.0", "r", "unsafe");

    expect(ch.sent.map((s) => s.evt.type)).toEqual([
      EventType.RequestFailed,
      EventType.RequestRejected,
    ]);
    expect(ch.sent[0].evt.payload).toEqual({ error: "boom" });
    expect(ch.sent[1].evt.payload).toEqual({ reason: "unsafe" });
  });

  it("replyUnknownTool answers with a request.failed naming the tool", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    await w.replyUnknownTool(parseToolCall(makeEvent({
      type: "nope",
      request_id: "req-9",
      trace_id: "t",
      payload: {},
    })));

    const { evt } = ch.sent[0];
    expect(evt.type).toBe(EventType.RequestFailed);
    expect(evt.request_id).toBe("req-9");
    expect(evt.payload).toEqual({ error: "unknown tool: nope" });
  });
});

describe("announceReady", () => {
  it("broadcasts worker.ready with the peer-facing contract, self excluded", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register(
      { event: "tool.invoke", description: "Echo" },
      () => {},
    );
    w.register(
      { event: "self.x", internal: true, description: "hidden" },
      () => {},
    );

    await w.announceReady("echo", [{ event: "echo.done" }]);

    expect(ch.broadcasts).toHaveLength(1);
    const presence = ch.broadcasts[0];
    expect(presence.type).toBe(EventType.WorkerReady);
    expect(presence.exclude_worker_id).toBe("echo.0");
    // Presence is live-only: it must not land in the durable event log.
    expect(presence.transient).toBe(true);
    expect(presence.payload).toEqual({
      type: "echo",
      watch: [{ event: "tool.invoke", desc: "Echo" }],
      publishes: [{ event: "echo.done" }],
    });
  });

  it("omits publishes when empty", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    await w.announceReady("echo");
    expect("publishes" in ch.broadcasts[0].payload).toBe(false);
  });

  it("announceReadyTo sends the contract to one target, transiently", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register({ event: "tool.invoke", description: "Echo" }, () => {});
    await w.announceReady("echo", [{ event: "echo.done" }]);
    ch.broadcasts = [];

    await w.announceReadyTo("asker.0");

    expect(ch.broadcasts).toHaveLength(0);
    expect(ch.sent).toHaveLength(1);
    const { evt, targets } = ch.sent[0];
    expect(targets).toEqual(["asker.0"]);
    expect(evt.type).toBe(EventType.WorkerReady);
    expect(evt.transient).toBe(true);
    // Directed presence is not a broadcast, so nothing is excluded.
    expect(evt.exclude_worker_id).toBeUndefined();
    expect(evt.payload).toMatchObject({
      type: "echo",
      watch: [{ event: "tool.invoke", desc: "Echo" }],
    });
  });
});

describe("worker.discover", () => {
  const tick = () => new Promise((r) => setImmediate(r));

  it("is answered with a directed worker.ready to the asker", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register(
      { event: "tool.invoke", description: "Echo" },
      () => {},
    );
    await w.announceReady("echo", [{ event: "echo.done" }]);
    ch.broadcasts = [];

    const handled = w.dispatchExtension(
      makeEvent({ type: EventType.WorkerDiscover, worker_id: "webui-hiw" }),
    );
    await tick();

    expect(handled).toBe(true);
    expect(ch.broadcasts).toHaveLength(0);
    expect(ch.sent).toHaveLength(1);
    const { evt, targets } = ch.sent[0];
    expect(targets).toEqual(["webui-hiw"]);
    expect(evt.type).toBe(EventType.WorkerReady);
    expect(evt.transient).toBe(true);
    expect(evt.payload).toMatchObject({
      type: "echo",
      watch: [{ event: "tool.invoke", desc: "Echo" }],
      publishes: [{ event: "echo.done" }],
    });
  });

  it("is not answered for itself", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    await w.announceReady("echo");
    ch.broadcasts = [];

    expect(
      w.dispatchExtension(
        makeEvent({ type: EventType.WorkerDiscover, worker_id: "echo.0" }),
      ),
    ).toBe(true);
    await tick();

    expect(ch.sent).toHaveLength(0);
  });

  it("is not answered before a contract is announced", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);

    expect(
      w.dispatchExtension(
        makeEvent({ type: EventType.WorkerDiscover, worker_id: "asker.0" }),
      ),
    ).toBe(true);
    await tick();

    expect(ch.sent).toHaveLength(0);
  });

  it("does not fall through to an extension registered for the event", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    const hits: string[] = [];
    w.register({ event: EventType.WorkerDiscover }, () => {
      hits.push("extension");
    });
    await w.announceReady("echo");

    w.dispatchExtension(
      makeEvent({ type: EventType.WorkerDiscover, worker_id: "asker.0" }),
    );
    await tick();

    expect(hits).toEqual([]);
    expect(ch.sent.map((s) => s.evt.type)).toEqual([EventType.WorkerReady]);
  });
});

describe("ready contract", () => {
  it("a registered schema (parameters + response) rides worker.ready", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register(
      {
        event: "echo",
        description: "Echo a message",
        parameters: { type: "object", properties: { text: { type: "string" } } },
        response: { type: "object", properties: { echoed: { type: "string" } } },
      },
      () => {},
    );
    w.register(
      { event: "quiet", description: "no schema", internal: true },
      () => {},
    );

    const entries = w.watchEntries(false);
    expect(entries).toHaveLength(1);
    expect(entries[0]["response"]).toEqual({
      type: "object",
      properties: { echoed: { type: "string" } },
    });

    await w.announceReady("echo");
    const presence = ch.broadcasts[ch.broadcasts.length - 1];
    const watch = presence.payload["watch"] as Record<string, unknown>[];
    expect(watch[0]["response"]).toBeDefined();
    expect(presence.exclude_worker_id).toBe(w.id);
  });
});

const tick = () => new Promise((r) => setImmediate(r));

describe("worker.readme", () => {
  it("answers with the declaration-customized guide", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.registerReadme("guide", () => "builtin", {
      readme: "hello {{name}}",
      name: "mcp",
    });

    const evt = makeEvent({ type: EventType.WorkerReadme, request_id: "r1" });
    expect(w.dispatchExtension(evt)).toBe(true);
    await tick();

    expect(ch.sent).toHaveLength(1);
    expect(ch.sent[0].evt.type).toBe(EventType.RequestCompleted);
    expect(ch.sent[0].evt.request_id).toBe("r1");
    expect(ch.sent[0].evt.payload["result"]).toBe("hello mcp");
  });

  it("appends, joins lists, and leaves unknown placeholders and JSON braces alone", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.registerReadme("guide", () => 'call { "x": 1 } and {{known}}', {
      readme_append: "extra {{tools}}",
      known: "yes",
      tools: ["a", "b"],
    });

    w.dispatchExtension(makeEvent({ type: EventType.WorkerReadme, request_id: "r1" }));
    await tick();

    expect(ch.sent[0].evt.payload["result"]).toBe(
      'call { "x": 1 } and yes\nextra a, b',
    );
  });
});

describe("worker.help", () => {
  const helpEvent = (
    payload: Record<string, unknown>,
    workerID = "caller.0",
  ): Event =>
    makeEvent({
      type: EventType.WorkerHelp,
      worker_id: workerID,
      request_id: "r1",
      payload,
    });

  it("lists served events when event_name is omitted", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register({ event: "spawn", description: "Create a new worker." }, () => {});
    w.registerHelp();

    w.dispatchExtension(helpEvent({}));
    await tick();

    const text = ch.sent[0].evt.payload["result"] as string;
    expect(text).toContain("# echo.0 — help");
    expect(text).toContain("`spawn`");
    expect(text).toContain("Create a new worker.");
    expect(text).toContain("`worker.help`");
  });

  it("renders the generic contract for a named event", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register(
      {
        event: "spawn",
        description: "Create a new worker.",
        parameters: { type: "object", properties: { type: { type: "string" } } },
        response: { type: "string" },
      },
      () => {},
    );
    w.registerHelp();

    w.dispatchExtension(helpEvent({ event_name: "spawn" }));
    await tick();

    const text = ch.sent[0].evt.payload["result"] as string;
    expect(text).toContain("# spawn");
    expect(text).toContain("Create a new worker.");
    expect(text).toContain("## Arguments");
    expect(text).toContain('"type": "string"');
    expect(text).toContain("## Reply");
  });

  it("selects a custom guide by params, and falls back when unmatched", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register({ event: "spawn", description: "Create a new worker." }, () => {});
    w.registerHelp({
      spawn: (params) =>
        params["worker"] === "reason" ? "# Spawn a reason worker" : undefined,
    });

    w.dispatchExtension(
      helpEvent({ event_name: "spawn", params: { worker: "reason" } }),
    );
    await tick();
    expect(ch.sent[0].evt.payload["result"]).toBe("# Spawn a reason worker");

    w.dispatchExtension(
      helpEvent({ event_name: "spawn", params: { worker: "timer" } }),
    );
    await tick();
    const fallback = ch.sent[1].evt.payload["result"] as string;
    expect(fallback).toContain("# spawn");
    expect(fallback).toContain("No event-specific guide matched");
  });

  it("fails an unknown event, naming what the worker serves", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register({ event: "spawn" }, () => {});
    w.registerHelp();

    w.dispatchExtension(helpEvent({ event_name: "nope" }));
    await tick();

    expect(ch.sent[0].evt.type).toBe(EventType.RequestFailed);
    const error = ch.sent[0].evt.payload["error"] as string;
    expect(error).toContain('does not serve event "nope"');
    expect(error).toContain("spawn");
  });

  it("hides internal capabilities from peers but shows them to the owner", async () => {
    const ch = new FakeChannel();
    const w = makeWorker(ch);
    w.register({ event: "my.op", internal: true }, () => {});
    w.registerHelp();

    w.dispatchExtension(helpEvent({}));
    await tick();
    const peerIndex = ch.sent[0].evt.payload["result"] as string;
    expect(peerIndex).not.toContain("my.op");

    w.dispatchExtension(helpEvent({}, w.id));
    await tick();
    const ownerIndex = ch.sent[1].evt.payload["result"] as string;
    expect(ownerIndex).toContain("my.op");
  });
});
