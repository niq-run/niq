import { describe, expect, it } from "vitest";
import { InterfaceWorker, ReplyError, type Backend, type Handler } from "./iface.js";
import { createEvent } from "./createEvent.js";
import { EventType, type Event, type WorkerSideChannel } from "./types/index.js";

/**
 * fakeChannel stands in for the bus: `send`/`broadcast` record, and the test
 * pushes inbound events through `push`.
 */
class fakeChannel implements WorkerSideChannel {
  readonly id = "iface-test";
  readonly sent: Event[] = [];
  readonly targets: string[][] = [];
  private readonly incoming: Event[] = [];
  private waiter?: () => void;
  private closed = false;

  async connect(): Promise<void> {}
  async send(evt: Event, ...targets: string[]): Promise<void> {
    this.sent.push(evt);
    this.targets.push(targets);
  }
  async broadcast(evt: Event): Promise<void> {
    this.sent.push(evt);
  }
  async *events(): AsyncIterable<Event> {
    while (!this.closed) {
      while (this.incoming.length > 0) {
        yield this.incoming.shift()!;
      }
      await new Promise<void>((resolve) => {
        this.waiter = resolve;
      });
    }
  }
  async close(): Promise<void> {
    this.closed = true;
    this.waiter?.();
  }

  push(evt: Event): void {
    this.incoming.push(evt);
    this.waiter?.();
    this.waiter = undefined;
  }
}

class fakeBackend implements Backend {
  handler?: Handler;
  setHandler(handler: Handler): void {
    this.handler = handler;
  }
}

async function start(): Promise<{ worker: InterfaceWorker; channel: fakeChannel; backend: fakeBackend }> {
  const channel = new fakeChannel();
  const backend = new fakeBackend();
  const worker = new InterfaceWorker({ id: "iface-test", channel, backends: [backend] });
  await worker.start();
  return { worker, channel, backend };
}

const wait = (ms = 10) => new Promise((r) => setTimeout(r, ms));

describe("InterfaceWorker", () => {
  it("injects the handler before serving, speaking as the worker", async () => {
    const { worker, backend } = await start();
    expect(backend.handler?.id).toBe("iface-test");
    await worker.stop();
  });

  it("fans inbound messages out by filter and isolates a throwing subscriber", async () => {
    const { worker, channel, backend } = await start();
    const seen: string[] = [];
    backend.handler!.onEvent(() => {
      throw new Error("boom");
    });
    backend.handler!.onEvent((evt) => seen.push(evt.type), [EventType.WorkerInput]);

    channel.push(createEvent(EventType.WorkerInput, { text: "hi" }));
    channel.push(createEvent("worker.ready", {}));
    await wait();

    expect(seen).toEqual([EventType.WorkerInput]);
    await worker.stop();
  });

  it("pairs a reply with its request and surfaces a failed reply as an error", async () => {
    const { worker, channel, backend } = await start();
    const h = backend.handler!;

    const ok = h.request("peer", createEvent("spawn", { type: "reason" }));
    await wait();
    const req = channel.sent[channel.sent.length - 1];
    expect(req.trace_id).toBeTruthy();
    expect(req.request_id).toBeTruthy();
    channel.push({ ...createEvent(EventType.RequestCompleted, { ok: true }), request_id: req.request_id });
    await expect(ok).resolves.toMatchObject({ type: EventType.RequestCompleted });

    const bad = h.request("peer", createEvent("spawn", {}));
    await wait();
    const req2 = channel.sent[channel.sent.length - 1];
    channel.push({
      ...createEvent(EventType.RequestFailed, { error: "nope" }),
      request_id: req2.request_id,
    });
    await expect(bad).rejects.toBeInstanceOf(ReplyError);
    await expect(bad).rejects.toThrow("nope");

    await worker.stop();
  });

  it("times out an unanswered request and fails in-flight ones on stop", async () => {
    const { worker, backend } = await start();
    const h = backend.handler!;
    await expect(h.request("peer", createEvent("spawn", {}), { timeoutMs: 20 })).rejects.toThrow(
      /timed out/,
    );

    const inflight = h.request("peer", createEvent("spawn", {}));
    await wait();
    await worker.stop();
    await expect(inflight).rejects.toThrow(/stopped/);
  });

  it("rejects a send without targets and forwards broadcast", async () => {
    const { worker, channel, backend } = await start();
    const h = backend.handler!;
    await expect(h.send(createEvent("worker.input", {}))).rejects.toThrow(/at least one target/);
    await h.broadcast(createEvent("worker.ready", {}));
    expect(channel.sent.some((e) => e.type === EventType.WorkerReady)).toBe(true);
    await worker.stop();
  });
});
